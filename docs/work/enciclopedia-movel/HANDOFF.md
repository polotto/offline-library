# Enciclopédia e dicionários no celular

Estado: implementação validada na prévia do Lab 0, à espera da publicação.

A busca inicial usa o índice de conteúdo do Kiwix. O Lab apresenta os
documentos da Wikipédia e do Wikcionário no endereço original, com
sumário e ficha recolhíveis. As tabelas largas têm rolagem própria.
Recursos e caminhos relativos continuam no mesmo acervo.

| Teste em Chrome móvel | Interface anterior | Prévia |
| --- | --- | --- |
| Busca por Curitiba e saudade na entrada | Campo filtra coleções | Retorna artigos e verbetes |
| Largura do artigo Curitiba em telas de 320 e 390 pixels | 510 pixels | 320 e 390 pixels |
| Início do texto de Curitiba | Cerca de 2.000 pixels abaixo do topo | A cerca de 396 pixels do topo |

O teste bloqueou origens externas e abriu uma imagem, a ficha e a
seção História no artigo Curitiba. Também abriu saudade em português,
casa em espanhol e home em inglês. A prévia usa os arquivos reais em
modo de leitura, com atualização desligada. O visualizador original
continua disponível em `/wiki/viewer`.
