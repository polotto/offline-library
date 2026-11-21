# Leitor e apontamentos

A barra é o catálogo em produção na mesma ficha, no computador e no celular. O leitor precisa abrir PDF (Portable Document Format) e EPUB (Electronic Publication) dentro do site, sem internet. Apontamentos devem guardar texto e posição de leitura por arquivo, acessíveis à conta que os escreveu.

- [x] Abrir anexos com navegação por página ou capítulo e preservar o download.
- [x] Criar, editar, excluir e reabrir apontamentos com posição. Guardar no catálogo existente.
- [x] Verificar em acervo isolado com arquivos reais e comparar as versões às cegas.
- [x] Preparar o pedido de integração para revisão.

O servidor local continua acessível pela rede. Cada apontamento guarda a página do PDF ou o identificador de posição no EPUB. O catálogo usa o banco SQLite atual, com autoria e arquivo conferidos em cada operação. Remover o anexo remove seus apontamentos. A ficha mantém o download e leva a uma página de leitura. A comparação usa Desbravando Java nas duas versões, com os mesmos arquivos, sem conexão externa, e verifica navegação, recarga e troca de conta.
