const leitor = document.querySelector("#leitor");
const documento = document.querySelector("#documento");
const estado = document.querySelector("#estado-leitor");
const anterior = document.querySelector("#anterior");
const proximo = document.querySelector("#proximo");
const menor = document.querySelector("#menor");
const maior = document.querySelector("#maior");
const posicao = document.querySelector("#posicao");
const guardar = document.querySelector("#guardar");
const notas = document.querySelectorAll("[data-posicao]");
const chave = `leitor:${location.pathname}`;

function ultimaPosicao() {
  try { return sessionStorage.getItem(chave); } catch { return null; }
}

function marcar(local) {
  if (posicao) {
    posicao.value = local;
    guardar.disabled = false;
  }
  try { sessionStorage.setItem(chave, local); } catch { /* armazenamento indisponível */ }
}

function falha() {
  estado.textContent = "Não foi possível abrir esta posição. Tente outra página ou baixe o arquivo.";
  if (guardar) guardar.disabled = true;
}

function ligar(botao, acao) {
  botao.disabled = false;
  botao.addEventListener("click", () => Promise.resolve().then(acao).catch(falha));
}

function ligarNotas(abrir) {
  for (const nota of notas) {
    ligar(nota, async () => {
      await abrir(nota.dataset.posicao);
      documento.focus();
    });
  }
}

async function lerPDF() {
  const pdfjs = await import("./vendor/pdf.min.mjs");
  pdfjs.GlobalWorkerOptions.workerSrc = "/catalogo/leitor/vendor/pdf.worker.min.mjs";
  const pasta = "/catalogo/leitor/vendor/";
  const livro = await pdfjs.getDocument({
    url: leitor.dataset.fonte,
    cMapUrl: pasta + "cmaps/", standardFontDataUrl: pasta + "standard_fonts/",
    wasmUrl: pasta + "wasm/", isEvalSupported: false,
  }).promise;
  const campo = document.querySelector("#pagina");
  campo.disabled = false;
  campo.max = livro.numPages;
  let atual = 1;
  let zoom = 1;
  let pedido = 0;

  async function abrir(numero) {
    const pagina = Math.min(livro.numPages, Math.max(1, Math.trunc(Number(numero)) || 1));
    const vez = ++pedido;
    if (guardar) guardar.disabled = true;
    atual = pagina;
    campo.value = pagina;
    anterior.disabled = pagina === 1;
    proximo.disabled = pagina === livro.numPages;
    const folha = await livro.getPage(pagina);
    if (vez !== pedido) return;
    const natural = folha.getViewport({ scale: 1 });
    const largura = Math.max(1, documento.clientWidth);
    const escala = largura / natural.width * zoom;
    const pixels = Math.min(devicePixelRatio || 1, 2, 4096 / (Math.max(natural.width, natural.height) * escala));
    const quadro = folha.getViewport({ scale: escala * pixels });
    const canvas = document.createElement("canvas");
    canvas.width = Math.ceil(quadro.width);
    canvas.height = Math.ceil(quadro.height);
    canvas.style.width = Math.ceil(quadro.width / pixels) + "px";
    canvas.setAttribute("aria-label", `Página ${pagina}`);
    await folha.render({ canvas, viewport: quadro }).promise;
    if (vez !== pedido) return;
    documento.replaceChildren(canvas);
    estado.textContent = `Página ${pagina} de ${livro.numPages}`;
    marcar(String(pagina));
  }

  ligar(anterior, () => abrir(atual - 1));
  ligar(proximo, () => abrir(atual + 1));
  ligar(menor, () => { zoom = Math.max(.75, zoom - .25); return abrir(atual); });
  ligar(maior, () => { zoom = Math.min(3, zoom + .25); return abrir(atual); });
  ligarNotas(abrir);
  campo.addEventListener("change", () => abrir(campo.value).catch(falha));
  let largura = documento.clientWidth;
  new ResizeObserver(() => {
    if (largura === documento.clientWidth) return;
    largura = documento.clientWidth;
    abrir(atual).catch(falha);
  }).observe(documento);
  await abrir(ultimaPosicao());
}

async function lerEPUB() {
  const livro = window.ePub();
  await livro.open(leitor.dataset.fonte, "epub");
  const navegacao = await livro.loaded.navigation;
  const leitura = livro.renderTo(documento, {
    width: "100%", height: "100%", spread: "none", allowScriptedContent: false,
  });
  const sumario = document.querySelector("#sumario");
  let tamanho = 100;
  const capitulos = new Map();
  leitura.on("relocated", (local) => {
    estado.textContent = capitulos.get(local.start.href) ||
      `Seção ${local.start.index + 1}`;
    anterior.disabled = local.atStart;
    proximo.disabled = local.atEnd;
    marcar(local.start.cfi);
  });
  leitura.on("displayerror", falha);
  function listar(itens, nivel = "") {
    for (const item of itens) {
      const titulo = item.label.trim();
      const arquivo = item.href.split("#")[0];
      if (!capitulos.has(arquivo)) capitulos.set(arquivo, titulo);
      sumario.add(new Option(nivel + titulo, item.href));
      listar(item.subitems || [], nivel + "  ");
    }
  }
  listar(navegacao.toc);
  sumario.hidden = sumario.options.length === 1;
  sumario.addEventListener("change", () => {
    if (sumario.value) {
      leitura.display(sumario.value).catch(falha);
      sumario.value = "";
    }
  });
  function fonte() {
    const edicao = leitura.currentLocation()?.start?.cfi;
    leitura.themes.fontSize(tamanho + "%");
    if (edicao) return leitura.display(edicao);
  }
  ligar(anterior, () => leitura.prev());
  ligar(proximo, () => leitura.next());
  ligar(menor, () => { tamanho = Math.max(75, tamanho - 10); return fonte(); });
  ligar(maior, () => { tamanho = Math.min(200, tamanho + 10); return fonte(); });
  ligarNotas((local) => leitura.display(local));
  const ultima = ultimaPosicao();
  try {
    await leitura.display(ultima || undefined);
  } catch (erro) {
    if (!ultima) throw erro;
    await leitura.display();
  }
}

(leitor.dataset.formato === "PDF" ? lerPDF() : lerEPUB()).catch(() => {
  estado.textContent = "Não foi possível carregar o arquivo. Volte à ficha para escolher outro.";
  if (guardar) guardar.disabled = true;
});
