const consulta = document.querySelector("#consulta");
const colecao = document.querySelector("#colecao");
const lista = document.querySelector("#sugestoes");

if (consulta && colecao && lista) {
  let espera;
  let pedido;

  function atualizar() {
    clearTimeout(espera);
    pedido?.abort();
    lista.replaceChildren();
    lista.hidden = true;
    const termo = consulta.value.trim();
    if (termo.length < 2) return;
    espera = setTimeout(() => sugerir(termo, colecao.value), 250);
  }

  async function sugerir(termo, acervo) {
    pedido = new AbortController();
    const { signal } = pedido;
    const params = new URLSearchParams({ content: acervo, term: termo, userlang: "pt" });
    try {
      const resposta = await fetch("/wiki/suggest?" + params, { signal });
      if (!resposta.ok) return;
      const sugestoes = await resposta.json();
      if (signal.aborted) return;
      for (const dica of sugestoes.filter(item => item.kind === "path").slice(0, 5)) {
        const segmentos = dica.path.split("/");
        if (segmentos.some(s => !s || s === "." || s === "..")) continue;
        const link = document.createElement("a");
        link.href = "/wiki/content/" + encodeURIComponent(acervo) + "/" +
          segmentos.map(encodeURIComponent).join("/") + "?leitura=1";
        link.textContent = dica.value;
        const item = document.createElement("li");
        item.append(link);
        lista.append(item);
      }
      lista.hidden = lista.childElementCount === 0;
    } catch {
      // O formulário continua disponível se o índice de sugestões estiver fora do ar.
    }
  }

  consulta.addEventListener("input", atualizar);
  colecao.addEventListener("change", atualizar);
}
