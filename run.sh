#!/usr/bin/env bash
# Uso: ./run.sh [build|dist|run|test|disco|lab0-test|lab0-logs|lab0-stop|lab0-busca|help]
set -euo pipefail
cd "$(dirname "$0")"
raiz=$(pwd -P)

# Configuração local fora do git (endereço do servidor de teste, pastas).
[ -f .env.local ] && . ./.env.local
LAB0_HOST=${LAB0_HOST:-}
LAB0_DADOS=${LAB0_DADOS:-/srv/offline/LabOffline}
LAB0_REPO=${LAB0_REPO:-offline-library-teste}
LAB0_PORTA=${LAB0_PORTA:-9087}
REPO_URL=${REPO_URL:-https://github.com/polotto/offline-library.git}
export CGO_ENABLED=0

# Go fixado pelo go.mod. Sem Go no sistema (ou mais antigo), baixa o
# oficial para .cache/go, dentro do projeto. Nada é instalado no sistema.
versao_go() { sed -n 's/^go \([0-9.]*\)$/\1/p' go.mod; }
garantir_go() {
  local quer atual so arq
  quer=$(versao_go)
  if command -v go >/dev/null; then
    atual=$(go env GOVERSION 2>/dev/null | sed 's/^go//')
    if [ "$(printf '%s\n%s\n' "$quer" "$atual" | sort -V | head -1)" = "$quer" ]; then GO=go; return; fi
  fi
  if [ ! -x .cache/go/bin/go ] || [ "$(.cache/go/bin/go env GOVERSION)" != "go$quer" ]; then
    so=$(uname -s | tr 'A-Z' 'a-z'); arq=$(uname -m)
    case "$arq" in x86_64) arq=amd64 ;; aarch64|arm64) arq=arm64 ;; esac
    echo "Baixando Go $quer para .cache/go"
    rm -rf .cache/go && mkdir -p .cache
    curl -fsSL "https://go.dev/dl/go$quer.$so-$arq.tar.gz" | tar -xz -C .cache
  fi
  export GOROOT="$raiz/.cache/go" GOTOOLCHAIN=local GOPATH="${GOPATH:-$raiz/.cache/gopath}"
  GO="$raiz/.cache/go/bin/go"
}
versao() { git describe --tags --always --dirty 2>/dev/null || echo dev; }
ldflags() { echo "-s -w -X github.com/polotto/offline-library/internal/lab.Versao=$(versao)"; }

compilar() {
  garantir_go
  mkdir -p bin
  "$GO" build -trimpath -ldflags "$(ldflags)" -o bin/lab ./cmd/lab
  echo "bin/lab ($(versao))"
}

dist() {
  garantir_go
  mkdir -p dist/bin
  local p so arq ext
  for p in linux/amd64 linux/arm64 darwin/arm64 darwin/amd64 windows/amd64; do
    so=${p%/*}; arq=${p#*/}; ext=; [ "$so" = windows ] && ext=.exe
    GOOS=$so GOARCH=$arq "$GO" build -trimpath -ldflags "$(ldflags)" -o "dist/bin/lab-$so-$arq$ext" ./cmd/lab
    echo "dist/bin/lab-$so-$arq$ext"
  done
}

# Monta ou atualiza um disco: executáveis, lançadores e programas de apoio.
disco() {
  local destino=${1:?informe a pasta do acervo no disco}
  [ -d "$destino" ] || { echo "Pasta não existe: $destino" >&2; exit 1; }
  dist
  mkdir -p "$destino/bin" "$destino/config"
  cp dist/bin/* "$destino/bin/"
  cp lancadores/iniciar.sh lancadores/iniciar.command lancadores/iniciar.bat lancadores/LEIA-ME.txt "$destino/"
  [ -f "$destino/config/lab.json" ] || cp exemplo/config/lab.json "$destino/config/lab.json"
  [ -f "$destino/config/acervo.txt" ] || cp exemplo/config/acervo.txt "$destino/config/acervo.txt"
  : > "$destino/.metadata_never_index"
  local proprio; proprio="dist/bin/lab-$(uname -s | tr 'A-Z' 'a-z')-$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')"
  "$proprio" preparar-runtime --dados "$destino" --plataformas "${PLATAFORMAS:-todas}"
  echo "Disco pronto em $destino"
}

exigir_lab0() { [ -n "$LAB0_HOST" ] || { echo "Defina LAB0_HOST em .env.local (veja .env.example)." >&2; exit 1; }; }

# Clona ou atualiza o repositório no servidor de teste, compila lá e sobe
# uma instância separada, numa porta própria, sem tocar no serviço atual.
lab0_test() {
  exigir_lab0
  local ref; ref=${1:-$(git rev-parse --abbrev-ref HEAD)}
  ssh "$LAB0_HOST" bash -s -- "$REPO_URL" "$LAB0_REPO" "$ref" "$LAB0_DADOS" "$LAB0_PORTA" <<'REMOTO'
set -euo pipefail
url=$1; dir=$2; ref=$3; dados=$4; porta=$5
cd ~
if [ -d "$dir/.git" ]; then git -C "$dir" fetch -q origin && git -C "$dir" checkout -q "$ref" && git -C "$dir" reset -q --hard "origin/$ref"
else git clone -q --branch "$ref" "$url" "$dir"; fi
cd "$dir"
echo "Commit em teste: $(git log --oneline -1)"
GOMAXPROCS=2 ./run.sh build
mkdir -p logs
if [ -f logs/lab.pid ] && kill -0 "$(cat logs/lab.pid)" 2>/dev/null; then kill "$(cat logs/lab.pid)"; sleep 2; fi
nohup ./bin/lab servir --dados "$dados" --porta "$porta" --logs "$PWD/logs" --sem-atualizar > logs/saida.log 2>&1 &
echo $! > logs/lab.pid
for i in $(seq 1 60); do
  curl -fs "http://127.0.0.1:$porta/estado.json" >/dev/null && break; sleep 1
done
curl -fs "http://127.0.0.1:$porta/estado.json" | head -c 1200; echo
REMOTO
  echo "Instância de teste: http://${LAB0_HOST#*@}:$LAB0_PORTA"
  echo "Logs: ./run.sh lab0-logs"
}

lab0_logs() {
  exigir_lab0
  ssh "$LAB0_HOST" "cd ~/$LAB0_REPO/logs 2>/dev/null && for f in saida.log lab.log kiwix.log rotas.log; do [ -f \$f ] && { echo \"== \$f\"; tail -n ${1:-40} \$f; }; done; echo '== memória'; free -h | sed -n 1,2p"
}

lab0_stop() {
  exigir_lab0
  ssh "$LAB0_HOST" "cd ~/$LAB0_REPO && [ -f logs/lab.pid ] && kill \$(cat logs/lab.pid) && rm logs/lab.pid && echo parado || echo 'nada rodando'"
}

# Gera o índice de busca no servidor de teste a partir do .osm.pbf já baixado.
lab0_busca() {
  exigir_lab0
  local pbf=${1:-$LAB0_DADOS/downloads/osm/south-america-latest.osm.pbf}
  ssh "$LAB0_HOST" "cd ~/$LAB0_REPO && nohup env GOMEMLIMIT=900MiB ./bin/lab gerar-busca --dados '$LAB0_DADOS' --pbf '$pbf' > logs/gerar-busca.log 2>&1 & echo 'Gerando; acompanhe com: ssh $LAB0_HOST tail -f ~/$LAB0_REPO/logs/gerar-busca.log'"
}

case "${1:-help}" in
  build) compilar ;;
  dist) dist ;;
  run) shift; compilar; exec ./bin/lab servir --dados "${LAB_DADOS:?defina LAB_DADOS com a pasta do acervo}" "$@" ;;
  test) garantir_go; "$GO" vet ./... && "$GO" test ./... ;;
  disco) shift; disco "$@" ;;
  lab0-test) shift; lab0_test "$@" ;;
  lab0-logs) shift; lab0_logs "$@" ;;
  lab0-stop) lab0_stop ;;
  lab0-busca) shift; lab0_busca "$@" ;;
  *) cat <<'AJUDA'
./run.sh build                compila bin/lab para este computador
./run.sh dist                 compila para Linux, Mac (Apple e Intel) e Windows em dist/bin
./run.sh run [opções]         compila e sobe com LAB_DADOS=pasta do acervo
./run.sh test                 go vet e testes
./run.sh disco PASTA          monta um disco: executáveis, lançadores e programas de apoio
./run.sh lab0-test [branch]   clona/atualiza no servidor de teste, compila e sobe na porta de teste
./run.sh lab0-logs [linhas]   mostra os logs da instância de teste
./run.sh lab0-stop            para a instância de teste
./run.sh lab0-busca [pbf]     gera o índice de busca no servidor de teste
Configuração local em .env.local (modelo em .env.example). Go é baixado
para .cache/go se o sistema não tiver a versão do go.mod.
AJUDA
  ;;
esac
