# AGENTS.md

Regras para quem trabalha neste repositório.

- Português do Brasil em código, interface e documentação.
- Repositório público: nunca versionar endereço da rede, usuário, nome da
  casa, senha ou caminho pessoal. Isso fica em `.env.local` e em
  `DADOS/config/lab.json`, fora do git.
- Sem cgo: `CGO_ENABLED=0` sempre. O binário precisa cruzar para Linux,
  Mac e Windows a partir de qualquer máquina (`./run.sh dist`).
- Nada pode exigir instalação no computador que usa o disco. Programa de
  apoio novo entra em `internal/lab/runtime.go`, com URL oficial e
  SHA-256 fixado, para as cinco plataformas.
- Disco exFAT: sem link simbólico, sem permissão confiável, sem WAL do
  SQLite. Ignorar arquivos `._*` do macOS.
- Uso de `run.sh`: `build`, `run`, `test`, `dist`, `disco`.

## Teste e depuração no servidor

1. `./run.sh lab0-test` clona ou atualiza no servidor, compila lá e sobe
   a instância de teste na porta `LAB0_PORTA`.
2. Abrir a instância no Playwright (navegador no computador de
   desenvolvimento, nunca no servidor, que tem pouca memória).
3. Cruzar o que a página mostra com `./run.sh lab0-logs`.
4. `./run.sh lab0-stop` ao terminar.

Commits em Conventional Commits sem escopo: `feat: ...`, `fix: ...`.
