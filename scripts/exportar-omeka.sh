#!/usr/bin/env bash
# Exporta itens do Omeka Classic (MariaDB ou MySQL) para o formato lido por
# "lab importar-omeka". Roda na máquina do Omeka.
#
#   OMEKA_DB=catalogo OMEKA_SOCKET=/caminho/mysql.sock bash exportar-omeka.sh > omeka.json
#
# Variáveis: OMEKA_DB (banco), OMEKA_SOCKET (soquete), OMEKA_USUARIO,
# OMEKA_PREFIXO (prefixo das tabelas, padrão omeka_), MYSQL (cliente).
set -euo pipefail
db=${OMEKA_DB:?defina OMEKA_DB}
p=${OMEKA_PREFIXO:-omeka_}
cliente=${MYSQL:-mariadb}
args=(--no-defaults -N -B --raw)
[ -n "${OMEKA_SOCKET:-}" ] && args+=(--socket="$OMEKA_SOCKET")
[ -n "${OMEKA_USUARIO:-}" ] && args+=(--user="$OMEKA_USUARIO")
[ "$cliente" = mariadb ] && args+=(--skip-ssl)
"$cliente" "${args[@]}" "$db" <<SQL
SET SESSION group_concat_max_len = 16777216;
SELECT COALESCE(JSON_ARRAYAGG(JSON_OBJECT(
  'id', i.id,
  'public', i.public,
  'campos', (SELECT JSON_OBJECTAGG(e.name, (
                SELECT JSON_ARRAYAGG(t2.text) FROM ${p}element_texts t2
                WHERE t2.record_type = 'Item' AND t2.record_id = i.id AND t2.element_id = e.id))
             FROM ${p}elements e
             WHERE e.id IN (SELECT t.element_id FROM ${p}element_texts t
                            WHERE t.record_type = 'Item' AND t.record_id = i.id)),
  'tags', (SELECT JSON_ARRAYAGG(tg.name) FROM ${p}records_tags rt JOIN ${p}tags tg ON tg.id = rt.tag_id
           WHERE rt.record_type = 'Item' AND rt.record_id = i.id),
  'arquivos', (SELECT JSON_ARRAYAGG(JSON_OBJECT('filename', f.filename, 'original', f.original_filename))
               FROM ${p}files f WHERE f.item_id = i.id)
)), '[]') FROM ${p}items i;
SQL
