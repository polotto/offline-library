#!/bin/sh
# Inicia a biblioteca a partir deste disco no Linux ou no Mac. Nada é instalado.
cd "$(dirname "$0")" || exit 1
so=$(uname -s | tr 'A-Z' 'a-z')
arq=$(uname -m)
case "$arq" in x86_64|amd64) arq=amd64 ;; aarch64|arm64) arq=arm64 ;; esac
exe="bin/lab-$so-$arq"
if [ ! -f "$exe" ]; then
  echo "Este disco não tem o programa para $so-$arq."
  exit 1
fi
# Disco exFAT pode não guardar permissão de execução: roda a partir de uma
# cópia local se for preciso.
if ! [ -x "$exe" ] || ! "$exe" versao >/dev/null 2>&1; then
  copia="${TMPDIR:-/tmp}/offline-library-$(id -u)"
  mkdir -p "$copia" && cp "$exe" "$copia/lab" && chmod +x "$copia/lab"
  chmod -R u+x runtime/"$so-$arq" 2>/dev/null
  exec env LAB_DADOS="$(pwd -P)" "$copia/lab" servir "$@"
fi
chmod -R u+x runtime/"$so-$arq" 2>/dev/null
exec "./$exe" servir --dados "$(pwd -P)" "$@"
