# offline-library

Biblioteca offline autocontida: enciclopédia (Wikipédia e Wikcionário em
pacotes Kiwix), mapas com busca de endereços e cálculo de caminhos, e uma
biblioteca de arquivos para livros e documentos. Tudo roda a partir de um
disco externo, sem instalar nada, em Linux, Mac (Apple e Intel) e Windows.

Um único programa em Go (`lab`) serve tudo numa porta só. Para a
enciclopédia e as rotas, ele inicia os programas oficiais (Kiwix e
BRouter com Java) que vão junto no disco, um por sistema.

| Endereço | O que é |
| --- | --- |
| `/` | Página inicial com a situação de cada parte e instruções de uso |
| `/wiki/` | Enciclopédia e dicionários (Kiwix) |
| `/mapas/` | Mapa da América do Sul, busca de lugares e caminhos |
| `/catalogo/` | Biblioteca de arquivos (busca, tags, cadastro com anexos) |

Só a própria máquina e a rede local acessam. Ver `redes_permitidas` em
`config/lab.json`.

## Usar um disco pronto

| Sistema | Como iniciar |
| --- | --- |
| Mac | clique duplo em `iniciar.command` |
| Windows | clique duplo em `iniciar.bat` |
| Linux | `sh iniciar.sh` na pasta do disco |

O navegador abre em `http://127.0.0.1:8087`. A janela mostra o endereço
para outros aparelhos da rede.

## Pasta de dados

```
LabOffline/
  iniciar.sh  iniciar.command  iniciar.bat  LEIA-ME.txt
  bin/lab-<sistema>-<arquitetura>[.exe]
  runtime/<sistema>-<arquitetura>/  kiwix-serve, jre/, pmtiles
  config/lab.json  config/acervo.txt
  kiwix/*.zim (+ .zim.ok depois de conferido)
  mapas/  america-sul.pmtiles, style.json, estilo-ruas.json, assets/, rotas/, busca.db
  catalogo-lab/  catalogo.db, arquivos/
  downloads/  estado/logs/
```

`config/lab.json` (modelo em `exemplo/config/`) define nome, porta,
redes permitidas e a hora da atualização automática. Com `"porta": 80`
o endereço abre sem digitar a porta; no Linux, o serviço precisa de
`AmbientCapabilities=CAP_NET_BIND_SERVICE` (já no exemplo em
`implantacao/lab.service`).

## Desenvolver

```sh
./run.sh build                 # bin/lab para este computador
./run.sh run                   # com LAB_DADOS=pasta do acervo
./run.sh dist                  # cinco sistemas em dist/bin
./run.sh disco /caminho/LabOffline   # executáveis, lançadores e programas de apoio
```

Sem Go no sistema, o `run.sh` baixa a versão do `go.mod` para
`.cache/go`, dentro do projeto. A compilação não usa cgo; o SQLite é Go
puro (`ncruces/go-sqlite3`).

## Testar num servidor

O fluxo clona o repositório no servidor, compila lá e sobe uma instância
numa porta separada, sem tocar no serviço que já roda.

```sh
cp .env.example .env.local     # endereço SSH e pasta do acervo do servidor
./run.sh lab0-test             # clona ou atualiza, compila e sobe
./run.sh lab0-logs             # logs do lab, Kiwix e rotas, e memória
./run.sh lab0-stop
```

Para depurar a interface, abrir o endereço da instância de teste no
Playwright e cruzar com `./run.sh lab0-logs`.

## Busca de lugares

O índice sai dos dados do OpenStreetMap (arquivo `.osm.pbf`, por padrão
o da América do Sul na Geofabrik). O gerador lê o arquivo duas vezes para
usar pouca memória. Com internet, o `lab` faz tudo sozinho uma vez por mês,
ou logo que falta o índice: baixa com retomada, confere o MD5 oficial,
gera, copia para `mapas/busca.db`, confere a cópia e só então troca o
índice em uso. Para forçar agora:

```sh
lab atualizar-busca --dados /caminho/LabOffline
```

Em `config/lab.json`, `osm` troca a região e `pasta_trabalho` põe os
arquivos grandes e temporários (cerca de 2,5 vezes o `.osm.pbf`) em outro
disco. Use uma pasta do disco interno quando o disco do acervo for USB
instável. Num servidor fixo cujo disco grava com defeito, `pasta_local`
põe no disco interno os programas de apoio desta plataforma e o índice de
busca; o acervo continua no disco. Os programas de apoio guardam o resumo
SHA-256 de cada arquivo na extração, e a conferência diária relê do disco
e refaz o que não bater. Geração à mão, sem internet:

```sh
lab gerar-busca --dados /caminho/LabOffline --pbf arquivo.osm.pbf --versao-busca nome-datado
```

Entram ruas, cidades e bairros, pontos com nome (farmácia, escola,
hospital...) e endereços com número. A busca não diferencia acentos e
aceita o começo das palavras.

## Ler no catálogo

Abra um item e escolha **Ler PDF** (Portable Document Format) ou
**Ler EPUB** (Electronic Publication). Os leitores funcionam com o servidor
local ligado e sem internet. Use **Baixar** para guardar o arquivo original.

Entre na sua conta para salvar apontamentos. Cada apontamento guarda a
página ou o trecho que estava aberto. Abra um apontamento para voltar ao local,
edite seu texto ou apague-o. Os apontamentos ficam no banco do catálogo e aparecem
apenas para a conta que os criou.

## Enciclopédia e dicionários

Em `/wiki/`, escolha uma coleção e pesquise no acervo local. O site abre
artigos e verbetes em uma página de leitura com sumário e tabelas que
rolam dentro da página. O Kiwix lê os arquivos ZIM (pacotes de páginas
offline) e consulta seus índices. Os artigos e a busca funcionam sem
internet enquanto o servidor local estiver disponível.

## Migrar do Omeka Classic

```sh
OMEKA_DB=catalogo bash scripts/exportar-omeka.sh > omeka.json
lab importar-omeka --dados /caminho/LabOffline --json omeka.json --arquivos /caminho/omeka/files/original
lab usuario --dados /caminho/LabOffline --nome admin   # senha pela entrada padrão
```

## Atualização do acervo

Com internet, o `lab` confere todo dia na hora configurada. Coleções Kiwix
de `config/acervo.txt` são conferidas por semana: download com retomada,
tamanho e SHA-256 oficiais, e só então entram no ar. O mapa é conferido por
mês, recortado da geração mais nova do Protomaps, e a busca de endereços
também. Se faltar um programa de apoio da plataforma em `runtime/`, ele é
baixado e conferido de novo. Sem internet ou sem espaço, nada muda.
`lab atualizar` faz uma rodada completa na hora.

## Licenças

Código sob licença MIT. Programas e dados baixados mantêm suas licenças:
Kiwix (GPLv3), Eclipse Temurin (GPLv2 com exceção de classpath), BRouter
(MIT), go-pmtiles e pmtiles.js (BSD-3), MapLibre GL JS (BSD-3), dados do
OpenStreetMap (ODbL, © colaboradores do OpenStreetMap), mapas Protomaps
e conteúdo da Wikipédia (CC BY-SA).

O leitor inclui PDF.js 5.7.284 (Apache 2.0), EPUB.js 0.3.93 (BSD-2) e
JSZip 3.10.2 (MIT ou GPLv3). Os termos e avisos dos arquivos distribuídos
estão em `web/catalogo/leitor/vendor/`. O arquivo `cmaps.zip` inclui
os mapas de caracteres do PDF.js e a licença original.
