/* Busca de endereços e lugares no índice local. Nada sai do servidor.
 *
 * Comportamento de caixa de busca de mapa:
 * - o campo funciona desde o início, mesmo antes do mapa abrir;
 * - sugestões enquanto digita, com barra fina de carregamento;
 * - respostas antigas nunca sobrescrevem as novas;
 * - X apaga o texto e o marcador e devolve o foco;
 * - tocar no campo depois de escolher seleciona o texto e reabre a lista;
 * - teclado: setas, Enter, Esc e Tab.
 */
const buscaUtil = (() => {
  function zoomDe(tipo) {
    if (tipo === 'Cidade') return 12;
    if (tipo === 'Vila' || tipo === 'Distrito' || tipo === 'Ilha') return 13;
    if (tipo === 'Bairro' || tipo === 'Povoado' || tipo === 'Localidade') return 14.5;
    if (tipo === 'Rodovia') return 13;
    if (tipo === 'Rua' || tipo === 'Avenida ou rua principal') return 16;
    return 17;
  }

  // Tira acento e caixa para comparar o que foi digitado com o nome.
  function normal(s) {
    return s.normalize('NFD').replace(/[̀-ͯ]/g, '').toLowerCase();
  }

  // Divide o texto em trechos [texto, destacado]. Destaca o começo de cada
  // palavra que bate com um termo digitado, sem ligar para acento.
  function trechos(texto, consulta) {
    const termos = normal(consulta).split(/[^\p{L}\p{N}]+/u).filter(Boolean);
    if (!termos.length || !texto) return [[texto, false]];
    const letras = [...texto];
    const base = letras.map(normal);
    if (base.some(c => c.length !== 1)) return [[texto, false]];
    const plano = base.join('');
    const marca = new Array(letras.length).fill(false);
    for (const t of termos) {
      const re = new RegExp('(^|[^\\p{L}\\p{N}])(' + t.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + ')', 'gu');
      let m;
      while ((m = re.exec(plano))) {
        const ini = m.index + m[1].length;
        for (let i = ini; i < ini + m[2].length; i++) marca[i] = true;
        if (re.lastIndex === m.index) re.lastIndex++;
      }
    }
    const saida = [];
    for (let i = 0; i < letras.length; i++) {
      const ult = saida[saida.length - 1];
      if (ult && ult[1] === marca[i]) ult[0] += letras[i];
      else saida.push([letras[i], marca[i]]);
    }
    return saida;
  }

  // Só vale a resposta do pedido mais novo.
  function sequenciador() {
    let ultimo = 0;
    return {novo: () => ++ultimo, vale: n => n === ultimo};
  }

  // Cache pequeno: o menos usado sai primeiro.
  function cache(limite) {
    const m = new Map();
    return {
      get(k) { if (!m.has(k)) return undefined; const v = m.get(k); m.delete(k); m.set(k, v); return v; },
      set(k, v) { m.delete(k); m.set(k, v); if (m.size > limite) m.delete(m.keys().next().value); },
    };
  }

  return {zoomDe, normal, trechos, sequenciador, cache};
})();

const instalarBusca = (() => {
  const form = document.getElementById('busca');
  const campo = document.getElementById('busca-texto');
  const lista = document.getElementById('busca-lista');
  const limpar = document.getElementById('busca-limpar');
  const aviso = document.getElementById('busca-aviso');
  const DEBOUNCE = 150, DEMORA = 2500, LIMITE = 20000, MINIMO = 2;
  const seq = buscaUtil.sequenciador();
  const guardados = buscaUtil.cache(60);
  let map = null, rotas = null, pendente = null, marcador = null;
  let pedido = null, espera = null;
  // achados vieram da consulta "digitado". textoBase é o que a pessoa
  // digitou, para voltar a ele quando as setas passam do fim da lista.
  let achados = [], digitado = '', parcial = false, textoBase = '', ativo = -1;
  let escolhido = null, enterPendente = false;
  let estado = 'pronto'; // pronto, carregando, erro, sem-indice
  let mensagem = '';
  let recentes = [];
  try { recentes = JSON.parse(localStorage.getItem('busca-recentes') || '[]').slice(0, 5); } catch (_) {}

  campo.disabled = false;

  // Pergunta se o índice existe sem travar o campo. 404 quer dizer
  // servidor antigo: a própria busca mostra se há índice.
  fetch('./busca/pronta', {cache: 'no-store'}).then(r => {
    if (r.status === 503) campo.placeholder = 'Busca de endereços ainda não instalada';
  }).catch(() => {});

  const anunciar = t => { aviso.textContent = t; };
  const carregando = sim => { form.dataset.carregando = String(sim); };
  const atualizarLimpar = () => { limpar.hidden = campo.value === ''; };
  const aberta = () => !lista.hidden;
  const consulta = () => textoBase.trim();
  const modoRecentes = () => consulta().length < MINIMO;
  const itensAtuais = () => modoRecentes() ? recentes : achados;

  function fechar() {
    lista.hidden = true;
    ativo = -1;
    campo.setAttribute('aria-expanded', 'false');
    campo.removeAttribute('aria-activedescendant');
  }

  function ir(l, comoRota) {
    if (!map) pendente = [l, comoRota];
    else {
      const ponto = [l.lon, l.lat];
      marcador?.remove();
      const el = document.createElement('div');
      el.className = 'busca-marcador';
      el.title = l.nome;
      marcador = new maplibregl.Marker({element: el, anchor: 'bottom'}).setLngLat(ponto).addTo(map);
      map.flyTo({center: ponto, zoom: buscaUtil.zoomDe(l.tipo), speed: 1.6});
      if (comoRota !== undefined && rotas) rotas.definir(comoRota, {lng: l.lon, lat: l.lat});
    }
    escolhido = {...l};
    enterPendente = false;
    campo.value = l.nome;
    textoBase = l.nome;
    atualizarLimpar();
    fechar();
    guardarRecente(l);
    anunciar(l.nome + ' no mapa' + (comoRota === 0 ? ', origem A' : comoRota === 1 ? ', destino B' : '') + '.');
    // No celular o teclado some e o mapa aparece.
    if (matchMedia('(pointer: coarse)').matches) campo.blur();
  }

  function guardarRecente(l) {
    const chave = l.nome + '|' + l.detalhe;
    const {nome, detalhe, tipo, lat, lon} = l;
    recentes = [{nome, detalhe, tipo, lat, lon}, ...recentes.filter(r => r.nome + '|' + r.detalhe !== chave)].slice(0, 5);
    try { localStorage.setItem('busca-recentes', JSON.stringify(recentes)); } catch (_) {}
  }

  function textoDestacado(texto, q) {
    return buscaUtil.trechos(texto || '', q).map(([t, forte]) => {
      if (!forte) return document.createTextNode(t);
      const b = document.createElement('mark');
      b.textContent = t;
      return b;
    });
  }

  function linhaInfo(classe, texto, botao) {
    const li = document.createElement('li');
    li.className = 'busca-info ' + classe;
    li.setAttribute('role', 'presentation');
    li.append(texto);
    if (botao) li.append(botao);
    return li;
  }

  function botao(texto, titulo, classe, acao) {
    const b = document.createElement('button');
    b.type = 'button';
    b.className = classe;
    if (typeof texto === 'string') b.textContent = texto; else b.append(...texto);
    if (titulo) { b.title = titulo; b.setAttribute('aria-label', titulo); }
    b.onclick = acao;
    return b;
  }

  // Desenha a lista com o estado atual. Os resultados anteriores ficam
  // na tela enquanto a nova resposta não chega, para não piscar.
  function desenhar() {
    const recente = modoRecentes();
    const fonte = itensAtuais();
    const itens = [];
    if (recente && fonte.length) itens.push(linhaInfo('busca-titulo', 'Recentes'));
    fonte.forEach((l, i) => {
      const li = document.createElement('li');
      li.setAttribute('role', 'option');
      li.id = 'busca-op-' + i;
      li.setAttribute('aria-selected', String(i === ativo));
      if (escolhido && l.nome === escolhido.nome && l.lat === escolhido.lat && l.lon === escolhido.lon) li.dataset.escolhido = 'true';
      const nome = document.createElement('span');
      nome.className = 'busca-nome';
      nome.append(...textoDestacado(l.nome, recente ? '' : digitado));
      const det = document.createElement('small');
      det.textContent = l.detalhe || l.tipo || '';
      li.append(
        botao([nome, det], '', 'busca-ir', () => ir(l)),
        botao('A', 'Usar ' + l.nome + ' como origem A', 'busca-ab', () => ir(l, 0)),
        botao('B', 'Usar ' + l.nome + ' como destino B', 'busca-ab', () => ir(l, 1)));
      itens.push(li);
    });
    if (!recente) {
      if (estado === 'erro') {
        itens.push(linhaInfo('busca-erro', mensagem, botao('Tentar de novo', '', 'busca-tentar', () => { campo.focus(); procurar(true); })));
      } else if (estado === 'sem-indice') {
        itens.push(linhaInfo('busca-erro', mensagem));
      } else if (estado === 'carregando' && mensagem) {
        itens.push(linhaInfo('busca-demora', mensagem));
      } else if (estado === 'pronto' && !achados.length && consulta() === digitado) {
        itens.push(linhaInfo('busca-vazio', 'Nada encontrado para "' + digitado + '". Tente o nome da rua com a cidade.'));
      } else if (estado === 'pronto' && parcial && achados.length) {
        itens.push(linhaInfo('busca-parcial', 'Resultados resumidos para responder rápido. Digite mais para refinar.'));
      }
    }
    lista.replaceChildren(...itens);
    if (!itens.length) { fechar(); return; }
    lista.hidden = false;
    campo.setAttribute('aria-expanded', 'true');
    if (ativo >= 0) {
      campo.setAttribute('aria-activedescendant', 'busca-op-' + ativo);
      document.getElementById('busca-op-' + ativo)?.scrollIntoView({block: 'nearest'});
    } else campo.removeAttribute('aria-activedescendant');
  }

  function chaveDe(q) {
    if (!map) return q;
    const c = map.getCenter();
    return q + '@' + c.lat.toFixed(1) + ',' + c.lng.toFixed(1);
  }

  function aplicar(q, d) {
    achados = d.lugares || [];
    parcial = !!d.parcial;
    estado = 'pronto';
    mensagem = '';
    digitado = q;
    ativo = -1;
    carregando(false);
    anunciar(achados.length ? achados.length + (achados.length === 1 ? ' resultado.' : ' resultados.') : 'Nada encontrado.');
    if (enterPendente) {
      enterPendente = false;
      if (achados.length) { ir(achados[0]); return; }
    }
    if (form.contains(document.activeElement)) desenhar();
  }

  function cancelar() {
    clearTimeout(espera);
    pedido?.abort();
    pedido = null;
    return seq.novo();
  }

  async function procurar(forcar) {
    const n = cancelar();
    const q = consulta();
    if (q.length < MINIMO) {
      carregando(false);
      estado = 'pronto'; mensagem = '';
      desenhar();
      return;
    }
    const chave = chaveDe(q);
    const salvo = forcar ? undefined : guardados.get(chave);
    if (salvo) { aplicar(q, salvo); return; }
    const controle = new AbortController();
    pedido = controle;
    estado = 'carregando'; mensagem = '';
    carregando(true);
    const demora = setTimeout(() => {
      if (!seq.vale(n)) return;
      mensagem = 'Ainda buscando. O servidor está lento, aguarde.';
      anunciar('Ainda buscando.');
      if (form.contains(document.activeElement)) desenhar();
    }, DEMORA);
    let porTempo = false;
    const corte = setTimeout(() => { porTempo = true; controle.abort(); }, LIMITE);
    const params = {q};
    if (map) {
      const c = map.getCenter();
      params.lat = c.lat.toFixed(4); params.lon = c.lng.toFixed(4);
    }
    try {
      const r = await fetch('./busca?' + new URLSearchParams(params), {signal: controle.signal, cache: 'no-store'});
      if (r.status === 503) throw Object.assign(new Error('sem índice'), {semIndice: true});
      if (!r.ok) throw new Error('HTTP ' + r.status);
      const d = await r.json();
      if (!d.parcial) guardados.set(chave, d);
      if (seq.vale(n)) aplicar(q, d);
    } catch (e) {
      if (!seq.vale(n)) return; // trocado por pedido mais novo ou apagado
      carregando(false);
      enterPendente = false;
      achados = []; digitado = q;
      if (e.semIndice) {
        estado = 'sem-indice';
        mensagem = 'A busca de endereços ainda não foi instalada neste servidor. O mapa funciona normalmente.';
      } else {
        estado = 'erro';
        mensagem = porTempo ? 'O servidor não respondeu a tempo.' : 'Não foi possível falar com o servidor do mapa.';
      }
      anunciar(mensagem);
      if (form.contains(document.activeElement)) desenhar();
    } finally {
      clearTimeout(corte);
      clearTimeout(demora);
      if (pedido === controle) pedido = null;
    }
  }

  function mover(passo) {
    const itens = itensAtuais();
    if (!itens.length) return;
    // Passar do fim volta ao texto digitado, como no Google Maps.
    let novo = ativo + passo;
    if (novo >= itens.length) novo = -1;
    else if (novo < -1) novo = itens.length - 1;
    ativo = novo;
    campo.value = ativo >= 0 ? itens[ativo].nome : textoBase;
    atualizarLimpar();
    desenhar();
  }

  function apagar() {
    cancelar();
    carregando(false);
    campo.value = '';
    textoBase = '';
    escolhido = null;
    achados = []; parcial = false; estado = 'pronto'; digitado = ''; enterPendente = false; mensagem = '';
    marcador?.remove();
    marcador = null;
    pendente = null;
    atualizarLimpar();
    campo.focus();
    desenhar();
    anunciar('Busca apagada.');
  }

  campo.addEventListener('input', () => {
    textoBase = campo.value;
    escolhido = null;
    ativo = -1;
    atualizarLimpar();
    const q = consulta();
    if (q.length < MINIMO || guardados.get(chaveDe(q))) { procurar(); return; }
    cancelar();
    estado = 'carregando'; mensagem = '';
    carregando(true);
    desenhar();
    espera = setTimeout(procurar, DEBOUNCE);
  });

  campo.addEventListener('keydown', e => {
    if (e.isComposing) return;
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      if (!aberta()) {
        if (!modoRecentes() && digitado !== consulta()) procurar();
        else desenhar();
        return;
      }
      mover(e.key === 'ArrowDown' ? 1 : -1);
    } else if (e.key === 'Escape') {
      e.preventDefault();
      if (aberta()) {
        if (ativo >= 0) { campo.value = textoBase; atualizarLimpar(); }
        fechar();
      } else if (campo.value) apagar();
      else campo.blur();
    } else if (e.key === 'Enter') {
      e.preventDefault();
      const itens = itensAtuais();
      if (aberta() && ativo >= 0 && itens[ativo]) { ir(itens[ativo]); return; }
      const q = consulta();
      if (q.length < MINIMO) return;
      if (estado === 'pronto' && digitado === q) {
        if (achados.length) ir(achados[0]);
        else desenhar();
        return;
      }
      if (estado === 'carregando') {
        // Escolhe o primeiro assim que a resposta chegar.
        enterPendente = true;
        if (!pedido) procurar();
      } else { enterPendente = true; procurar(true); }
    }
  });

  // Dentro da lista, as setas andam entre as linhas na mesma coluna.
  lista.addEventListener('keydown', e => {
    const atual = e.target.closest('button');
    if (!atual) return;
    if (e.key === 'Escape') { e.preventDefault(); fechar(); campo.focus(); return; }
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
    e.preventDefault();
    const linhas = [...lista.querySelectorAll('li[role=option]')];
    const li = atual.closest('li');
    const col = [...li.querySelectorAll('button')].indexOf(atual);
    const alvo = linhas[linhas.indexOf(li) + (e.key === 'ArrowDown' ? 1 : -1)];
    if (alvo) alvo.querySelectorAll('button')[col]?.focus();
    else if (e.key === 'ArrowUp') campo.focus();
  });

  // Tocar no campo depois de escolher seleciona tudo e reabre a lista.
  // O primeiro clique depois de escolher seleciona tudo, com ou sem foco.
  // O clique roda depois do mouseup, então o navegador não desfaz a seleção.
  const selecionarTudo = () => campo.setSelectionRange(0, campo.value.length);
  campo.addEventListener('click', () => {
    if (escolhido && !escolhido.selecionado) { escolhido.selecionado = true; selecionarTudo(); }
    if (!aberta()) desenhar();
  });
  campo.addEventListener('focus', () => {
    if (escolhido) selecionarTudo(); // foco pelo teclado
    if (aberta()) return;
    if (modoRecentes() || estado === 'carregando') { desenhar(); return; }
    if (escolhido && achados.length) { desenhar(); return; }
    if (digitado === consulta() && estado !== 'erro') { desenhar(); return; }
    procurar();
  });

  limpar.addEventListener('click', apagar);
  // Clique na lista ou no X não tira o foco do campo antes da hora.
  lista.addEventListener('mousedown', e => e.preventDefault());
  limpar.addEventListener('mousedown', e => e.preventDefault());

  form.addEventListener('submit', e => e.preventDefault());
  form.addEventListener('focusout', e => {
    if (!form.contains(e.relatedTarget)) fechar();
  });
  document.addEventListener('pointerdown', e => { if (!form.contains(e.target)) fechar(); });

  atualizarLimpar();

  // Chamado quando o mapa termina de abrir.
  return function instalar(m, r) {
    map = m;
    rotas = r;
    if (pendente) { const [l, comoRota] = pendente; pendente = null; ir(l, comoRota); }
  };
})();
