# Leitor e apontamentos

Estado: implementação local verificada e publicada no [pedido de
integração 2](https://github.com/polotto/offline-library/pull/2). A ficha dá acesso ao leitor e ao
download. O leitor usa dependências embutidas e a sessão do catálogo. Cada
apontamento pertence à conta e ao anexo e guarda a página do documento ou
a posição canônica no livro. O banco existente ganha uma tabela na abertura.

A linha de base é o item Desbravando Java no catálogo em produção. O
documento abre no visualizador do navegador e o livro aciona download.
A cópia isolada usa os mesmos anexos. No Chrome, em 1440 e 390 pixels,
houve leitura dos dois formatos e retorno a posições distintas em cada
um. O acesso externo foi bloqueado. O teste do servidor cobre isolamento
de apontamentos por conta e arquivo e remoção em cascata.

Nenhum dado do acervo de teste faz parte do repositório.

O crítico independente escolheu o leitor integrado nas duas larguras.
Ele encontrou mensagens confusas para anexos ausentes. O reteste confirmou
a mensagem de falha e o caminho de volta nos dois formatos. A comparação
dos resumos SHA-256 (Secure Hash Algorithm de 256 bits) confirmou que os
três downloads são idênticos aos originais. As cinco compilações passaram.

Para repetir, importe o item Desbravando Java ou cadastre as duas versões
como anexos de um item em uma pasta de dados isolada. Em 1440 e 390 pixels,
bloqueie as requisições a outras origens. Abra as páginas 3 e 4 do documento
e dois capítulos distintos do livro pelo sumário. Salve uma nota em cada
posição, recarregue e use o botão de cada nota para verificar o destino.
Entre com uma segunda conta e confira que a lista fica vazia. Em outra
ficha de teste, remova os arquivos do disco para reproduzir a falha de
abertura sem apagar o cadastro.

| Verificação no Chrome | 1440 pixels | 390 pixels |
| --- | --- | --- |
| Documento, notas nas páginas 3 e 4 e volta à página 3 | Passou | Passou |
| Livro, notas em duas seções e volta à posição original | Passou | Passou |
| Edição preserva a posição e exclusão persiste após recarga nos dois formatos | Passou | Passou |
| Arquivos ausentes informam falha nos dois formatos | Passou | Passou |
| Comparação cega com o mesmo item | Leitor integrado | Leitor integrado |

O navegador abriu o livro também com a resposta do sumário atrasada em
600 milissegundos. O catálogo reabriu com cinco registros do acervo de
teste após a criação da tabela de apontamentos em uma cópia sem a tabela.

Os mapas de caracteres foram empacotados com a licença para que a
verificação do GitHub possa examinar a lista completa de arquivos. A rota
continua entregando cada mapa pelo nome original.

A revisão remota apontou que a falha de um capítulo chega em
`displayerror`. O teste removeu um capítulo de uma cópia do livro.
O leitor mostrou a falha e voltou a abrir outro capítulo pelo sumário.
