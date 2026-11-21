# Enciclopédia e dicionários no celular

A barra é o catálogo do Kiwix e o visualizador do Lab 0 com Curitiba e
saudade, em 320 e 390 pixels. A interface própria abre os artigos em
uma página sem quadro interno e aproveita os arquivos e o índice do
Kiwix. A navegação preserva links, figuras, referências e busca offline.

- [x] Página inicial com busca e coleções instaladas.
- [x] Conteúdo legível em 320 e 390 pixels e tabelas largas com rolagem local.
- [x] Comparação independente com o catálogo e o visualizador atuais.
- [x] Preparar a publicação no Lab 0.

A busca da tela antiga só filtrava coleções. A resposta de Curitiba
excedia a largura disponível em 120 a 190 pixels. O novo início reutiliza
`ZimsProntos` e a busca em `/wiki/search`. O leitor adapta respostas de
`/wiki/content` no mesmo caminho para manter imagens, links e âncoras.
O parâmetro `leitura=1` distingue a apresentação própria do visualizador
original. As sugestões consultam o índice de títulos do Kiwix.
Figuras e demais recursos continuam pelo encaminhamento existente.
A comparação usa as mesmas coleções em instâncias separadas, bloqueia
origens externas e confere busca, figuras, referências e rolagem.
