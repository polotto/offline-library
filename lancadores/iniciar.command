#!/bin/sh
# Mac: clique duplo neste arquivo. Abre o Terminal com a biblioteca rodando
# e o navegador na página inicial. Feche a janela para parar.
cd "$(dirname "$0")" || exit 1
# Arquivos copiados pela internet ganham quarentena; tira só destas pastas.
xattr -dr com.apple.quarantine bin runtime 2>/dev/null
(sleep 4; open "http://127.0.0.1:8087/") &
exec sh ./iniciar.sh "$@"
