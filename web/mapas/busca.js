/* Busca de endereços e lugares no índice local. Nada sai do servidor.
 *
 * Comportamento de caixa de busca de mapa:
 * - o campo funciona desde o início, mesmo antes do mapa abrir;
 * - a lista acompanha a digitação: no máximo um pedido em andamento, sem
 *   cancelar por tecla nova; quando ele volta, a lista muda e o pedido do
 *   texto mais novo sai na hora;
 * - enquanto espera, a última lista fica na tela, filtrada pelo texto atual;
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
    if (tipo === 'Endereço') return 18;
    return 17;
  }

  // Tira acento e caixa para comparar o que foi digitado com o nome.
  function normal(s) {
    return s.normalize('NFD').replace(/[̀-ͯ]/g, '').toLowerCase();
  }
  const termosDe = q => normal(q).split(/[^\p{L}\p{N}]+/u).filter(Boolean);

  // Divide o texto em trechos [texto, destacado]. Destaca o começo de cada
  // palavra que bate com um termo digitado, sem ligar para acento.
  function trechos(texto, consulta) {
    const termos = termosDe(consulta);
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

  // O lugar ainda serve para o texto atual? Segue as regras do servidor
  // (montarConsulta em internal/lab/busca.go), mas do lado folgado: o
  // filtro local só tira o que claramente deixou de casar.
  // - "de", "da", "do", "dos", "das" e "e" não contam;
  // - abreviação vale pela forma por extenso ("av" acha "avenida");
  // - palavra de 5 letras ou mais vale nos dois gêneros ("rodoviaria" acha
  //   "rodoviario");
  // - o tipo conta como palavra; termo de 1 letra não decide nada;
  // - o número da casa filtra endereços numerados e mantém ruas sem número;
  // - qualquer termo vale como começo de palavra (o servidor exige palavra
  //   inteira nos termos do meio; aqui ser folgado só mantém mais itens).
  const VAZIAS = new Set(['de', 'da', 'do', 'dos', 'das', 'e']);
  const ABREVIACOES = {av: 'avenida', r: 'rua', al: 'alameda', pca: 'praca', tv: 'travessa',
    trav: 'travessa', est: 'estrada', rod: 'rodovia', lgo: 'largo', dr: 'doutor', prof: 'professor',
    pres: 'presidente', cel: 'coronel', mal: 'marechal', gen: 'general', sen: 'senador',
    gov: 'governador', sta: 'santa', sto: 'santo', eng: 'engenheiro', dep: 'deputado'};
  function casa(l, consulta) {
    const numero = consulta.match(/(?:^|\s)(\d+)(?:\s+\p{L})?$/u)?.[1];
    const numeroDoLugar = l.nome?.match(/,\s*(\d+)$/)?.[1];
    if (numero && numeroDoLugar && !numeroDoLugar.startsWith(numero)) return false;
    const termos = termosDe(consulta).filter(t => t.length > 1 && !VAZIAS.has(t) && !/\d/.test(t));
    if (!termos.length) return true;
    const palavras = termosDe([l.nome, l.tipo, l.detalhe].filter(Boolean).join(' '));
    return termos.every(t => {
      const formas = [t.length >= 5 && /[ao]$/.test(t) ? t.slice(0, -1) : t];
      if (ABREVIACOES[t]) formas.push(ABREVIACOES[t]);
      return palavras.some(p => formas.some(f => p.startsWith(f)));
    });
  }

  // O texto novo continua o texto velho? Então a resposta velha ainda ajuda.
  const continua = (novo, velho) => normal(novo).startsWith(normal(velho));

  // Cache pequeno: o menos usado sai primeiro.
  function cache(limite) {
    const m = new Map();
    return {
      get(k) { if (!m.has(k)) return undefined; const v = m.get(k); m.delete(k); m.set(k, v); return v; },
      set(k, v) { m.delete(k); m.set(k, v); if (m.size > limite) m.delete(m.keys().next().value); },
      delete(k) { m.delete(k); },
    };
  }

  return {zoomDe, normal, trechos, casa, continua, cache};
})();

const instalarBusca = (() => {
  const form = document.getElementById('busca');
  const campo = document.getElementById('busca-texto');
  const lista = document.getElementById('busca-lista');
  const limpar = document.getElementById('busca-limpar');
  const aviso = document.getElementById('busca-aviso');
  const DEMORA = 2500, LIMITE = 20000, MINIMO = 2, VALIDADE_PARCIAL = 60000;
  const guardados = buscaUtil.cache(80);
  let map = null, rotas = null, pendente = null, marcador = null;
  // base: última resposta mostrada {q, lugares, parcial, n}.
  // voo: o único pedido em andamento {q, n, controle, inicio}.
  // textoBase: o que a pessoa digitou, para voltar a ele com as setas.
  let base = {q: '', lugares: [], parcial: false, n: 0};
  let voo = null, numero = 0;
  let achados = [], textoBase = '', ativo = -1;
  let escolhido = null, enterPendente = false, listaDaEscolha = false;
  let falha = null; // {q, tipo: 'erro' | 'sem-indice', mensagem}
  let recentes = [];
  try { recentes = JSON.parse(localStorage.getItem('busca-recentes') || '[]').slice(0, 5); } catch (_) {}

  campo.disabled = false;

  // Pergunta se o índice existe sem travar o campo. 404 quer dizer
  // servidor antigo: a própria busca mostra se há índice.
  fetch('./busca/pronta', {cache: 'no-store'}).then(r => {
    if (r.status === 503) campo.placeholder = 'Busca de endereços ainda não instalada';
  }).catch(() => {});

  const anunciar = t => { if (aviso.textContent !== t) aviso.textContent = t; };
  const atualizarLimpar = () => { limpar.hidden = campo.value === ''; };
  const aberta = () => !lista.hidden;
  const consulta = () => textoBase.trim();
  const modoRecentes = () => consulta().length < MINIMO;
  const focado = () => form.contains(document.activeElement);
  const atualizarCarregando = () => { form.dataset.carregando = String(!!voo); };

  function fechar() {
    lista.hidden = true;
    ativo = -1;
    campo.setAttribute('aria-expanded', 'false');
    campo.removeAttribute('aria-activedescendant');
  }

  function tirarMarcador() {
    marcador?.remove();
    marcador = null;
    pendente = null;
    escolhido = null;
    listaDaEscolha = false;
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
    abortar();
    escolhido = {...l};
    enterPendente = false;
    listaDaEscolha = false;
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
    li.dataset.assinatura = classe + '|' + texto;
    return li;
  }

  function botao(conteudo, titulo, classe, acao) {
    const b = document.createElement('button');
    b.type = 'button';
    b.className = classe;
    // Padrão de combobox: as opções não são paradas do Tab. Tab sai do
    // campo e fecha a lista; as setas, Enter e Alt+A ou Alt+B escolhem.
    b.tabIndex = -1;
    b.append(...conteudo);
    if (titulo) { b.title = titulo; b.setAttribute('aria-label', titulo); }
    b.onclick = acao;
    return b;
  }

  function rotulo(letra, texto) {
    const l = document.createElement('b');
    l.textContent = letra;
    const t = document.createElement('small');
    t.textContent = texto;
    return [l, t];
  }

  // Quais lugares mostrar agora. Se a última resposta é de um texto mais
  // curto, filtra no navegador o que ainda casa. Se nada casa e há pedido
  // em andamento, mantém a lista velha esmaecida até a nova chegar.
  function lugaresAgora() {
    const q = consulta();
    if (listaDaEscolha) return {itens: base.lugares, velha: false};
    if (modoRecentes()) return {itens: recentes, velha: false};
    if (base.q === q) return {itens: base.lugares, velha: false};
    if (!base.q) return {itens: [], velha: false};
    const filtrados = base.lugares.filter(l => buscaUtil.casa(l, q));
    if (filtrados.length || !voo) return {itens: filtrados, velha: false};
    return {itens: base.lugares, velha: true};
  }

  // Troca só as linhas que mudaram. Quando a resposta do servidor traz o
  // mesmo lugar no topo, o topo fica intacto e não pisca.
  function aplicarLinhas(novas) {
    const velhas = [...lista.children];
    novas.forEach((li, i) => {
      const atual = velhas[i];
      if (!atual) lista.append(li);
      else if (atual.dataset.assinatura !== li.dataset.assinatura) atual.replaceWith(li);
    });
    velhas.slice(novas.length).forEach(li => li.remove());
  }

  // Desenha a lista com o estado atual, sem esconder durante a espera.
  function desenhar() {
    const q = listaDaEscolha ? base.q : consulta();
    const recente = !listaDaEscolha && modoRecentes();
    const {itens: fonte, velha} = lugaresAgora();
    achados = fonte;
    if (ativo >= fonte.length) ativo = -1;
    const itens = [];
    if (recente && fonte.length) itens.push(linhaInfo('busca-titulo', 'Recentes'));
    fonte.forEach((l, i) => {
      const li = document.createElement('li');
      li.setAttribute('role', 'option');
      li.id = 'busca-op-' + i;
      li.setAttribute('aria-selected', String(i === ativo));
      if (velha) li.className = 'busca-velha';
      const marcado = !!escolhido && l.nome === escolhido.nome && l.lat === escolhido.lat && l.lon === escolhido.lon;
      if (marcado) li.dataset.escolhido = 'true';
      // Linha igual à que já está na tela não é refeita (ver aplicarLinhas).
      li.dataset.assinatura = [i, l.nome, l.detalhe, l.lat, l.lon, recente ? '' : q, velha, marcado, i === ativo].join('|');
      const nome = document.createElement('span');
      nome.className = 'busca-nome';
      nome.append(...textoDestacado(l.nome, recente ? '' : q));
      const det = document.createElement('small');
      det.textContent = l.detalhe || l.tipo || '';
      li.append(
        botao([nome, det], '', 'busca-ir', () => ir(l)),
        botao(rotulo('A', 'Origem'), 'Usar ' + l.nome + ' como origem A (Alt+A)', 'busca-ab', () => ir(l, 0)),
        botao(rotulo('B', 'Destino'), 'Usar ' + l.nome + ' como destino B (Alt+B)', 'busca-ab', () => ir(l, 1)));
      itens.push(li);
    });
    if (!recente) {
      if (falha && falha.q === q) {
        const tentar = falha.tipo === 'erro'
          ? botao(['Tentar de novo'], '', 'busca-tentar', () => { campo.focus(); falha = null; pedir(q); desenhar(); })
          : null;
        itens.push(linhaInfo('busca-erro', falha.mensagem, tentar));
      } else if (voo && performance.now() - voo.inicio > DEMORA) {
        itens.push(linhaInfo('busca-demora', 'Ainda buscando. O servidor está lento, aguarde.'));
      } else if (!voo && base.q === q && !fonte.length && !base.parcial) {
        itens.push(linhaInfo('busca-vazio', 'Nada encontrado para "' + q + '". Tente o nome da rua com a cidade.'));
      } else if (base.q === q && base.parcial && fonte.length) {
        itens.push(linhaInfo('busca-parcial', 'Resultados resumidos para responder rápido. Digite mais para refinar.'));
      }
    }
    aplicarLinhas(itens);
    if (!itens.length) { fechar(); return; }
    lista.hidden = false;
    campo.setAttribute('aria-expanded', 'true');
    if (ativo >= 0) {
      campo.setAttribute('aria-activedescendant', 'busca-op-' + ativo);
      document.getElementById('busca-op-' + ativo)?.scrollIntoView({block: 'nearest'});
    } else campo.removeAttribute('aria-activedescendant');
  }

  function chaveDe(q) {
    const n = buscaUtil.normal(q);
    if (!map) return n;
    const c = map.getCenter();
    return n + '@' + c.lat.toFixed(1) + ',' + c.lng.toFixed(1);
  }

  function lerCache(q) {
    const v = guardados.get(chaveDe(q));
    if (!v) return undefined;
    if (v.d.parcial && Date.now() - v.t > VALIDADE_PARCIAL) { guardados.delete(chaveDe(q)); return undefined; }
    return v.d;
  }

  // Mostra a resposta de q, se ela não é mais velha que a já mostrada e se
  // o texto atual ainda continua q.
  function aplicar(q, d, n) {
    if (n < base.n) return;
    if (!buscaUtil.continua(consulta(), q)) return;
    // Resposta resumida e vazia de prefixo curto não apaga a lista que serve.
    let lugares = d.lugares || [];
    if (d.parcial && !lugares.length && base.lugares.some(l => buscaUtil.casa(l, consulta()))) return;
    // Resposta resumida (parcial) é incompleta e tem ordem menos firme: o
    // lugar que já está no topo e ainda casa com o texto continua no topo,
    // mesmo que a resposta resumida não o traga. Resposta completa manda.
    const topo = achados[0];
    if (d.parcial && topo && !listaDaEscolha && !modoRecentes() && buscaUtil.casa(topo, consulta())) {
      const outros = lugares.filter(l => !(l.nome === topo.nome && l.lat === topo.lat && l.lon === topo.lon));
      lugares = [topo, ...outros].slice(0, Math.max(lugares.length, 1));
    }
    base = {q, lugares, parcial: !!d.parcial, n};
    if (falha && falha.q === q) falha = null;
    if (q === consulta()) {
      const total = base.lugares.length;
      anunciar(total ? total + (total === 1 ? ' resultado.' : ' resultados.') : 'Nada encontrado.');
      if (enterPendente) {
        enterPendente = false;
        if (total) { ir(base.lugares[0]); return; }
      }
    }
    // Quem está andando com as setas não vê a lista mudar debaixo do dedo.
    if (focado() && ativo < 0) desenhar();
  }

  function abortar() {
    if (!voo) return;
    voo.controle.abort();
    voo = null;
    atualizarCarregando();
  }

  // Decide o próximo pedido. Nunca cancela um pedido que o texto atual
  // ainda continua: espera ele voltar e então pede o texto mais novo.
  function agendar() {
    const q = consulta();
    if (q.length < MINIMO) { abortar(); enterPendente = false; return; }
    const salvo = lerCache(q);
    if (salvo) { aplicar(q, salvo, ++numero); return; }
    if (voo) {
      if (voo.q === q || buscaUtil.continua(q, voo.q)) return;
      abortar(); // apagou letras: a resposta em andamento não serve mais
    }
    pedir(q);
  }

  async function pedir(q) {
    abortar();
    const n = ++numero;
    const controle = new AbortController();
    const meu = {q, n, controle, inicio: performance.now()};
    voo = meu;
    atualizarCarregando();
    const demora = setTimeout(() => { if (voo === meu && focado()) { anunciar('Ainda buscando.'); desenhar(); } }, DEMORA + 50);
    let porTempo = false;
    const corte = setTimeout(() => { porTempo = true; controle.abort(); }, LIMITE);
    const params = {q};
    if (map) {
      const c = map.getCenter();
      params.lat = c.lat.toFixed(4); params.lon = c.lng.toFixed(4);
    }
    let resposta = null;
    try {
      const r = await fetch('./busca?' + new URLSearchParams(params), {signal: controle.signal, cache: 'no-store'});
      if (r.status === 503) throw Object.assign(new Error('sem índice'), {semIndice: true});
      if (!r.ok) throw new Error('HTTP ' + r.status);
      resposta = await r.json();
      guardados.set(chaveDe(q), {d: resposta, t: Date.now()});
    } catch (e) {
      if (controle.signal.aborted && !porTempo) return; // abandonado de propósito
      if (voo === meu && q === consulta()) {
        enterPendente = false;
        falha = e.semIndice
          ? {q, tipo: 'sem-indice', mensagem: 'A busca de endereços ainda não foi instalada neste servidor. O mapa funciona normalmente.'}
          : {q, tipo: 'erro', mensagem: porTempo ? 'O servidor não respondeu a tempo.' : 'Não foi possível falar com o servidor do mapa.'};
        anunciar(falha.mensagem);
      }
    } finally {
      clearTimeout(corte);
      clearTimeout(demora);
    }
    if (voo !== meu) return;
    voo = null;
    atualizarCarregando();
    if (resposta) aplicar(q, resposta, n);
    if (consulta() !== q && !falha) agendar(); // texto mudou enquanto esperava
    if (focado() && ativo < 0) desenhar();
  }

  function mover(passo) {
    const itens = achados;
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
    abortar();
    campo.value = '';
    textoBase = '';
    base = {q: '', lugares: [], parcial: false, n: base.n};
    enterPendente = false;
    falha = null;
    tirarMarcador();
    atualizarLimpar();
    campo.focus();
    desenhar();
    anunciar('Busca apagada.');
  }

  campo.addEventListener('input', () => {
    textoBase = campo.value;
    listaDaEscolha = false;
    ativo = -1;
    escolhido = null;
    // Apagar tudo com Backspace limpa como o X.
    if (campo.value === '') tirarMarcador();
    if (falha && falha.q !== consulta()) falha = null;
    atualizarLimpar();
    agendar();
    desenhar();
  });

  campo.addEventListener('keydown', e => {
    if (e.isComposing) return;
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      if (!aberta()) { agendar(); desenhar(); return; }
      mover(e.key === 'ArrowDown' ? 1 : -1);
    } else if (e.key === 'Tab') {
      fechar();
    } else if (e.altKey && (e.code === 'KeyA' || e.code === 'KeyB') && aberta() && ativo >= 0 && achados[ativo]) {
      e.preventDefault();
      ir(achados[ativo], e.code === 'KeyA' ? 0 : 1);
    } else if (e.key === 'Escape') {
      e.preventDefault();
      if (aberta()) {
        if (ativo >= 0) { campo.value = textoBase; atualizarLimpar(); }
        fechar();
      } else if (campo.value) apagar();
      else campo.blur();
    } else if (e.key === 'Enter') {
      e.preventDefault();
      if (aberta() && ativo >= 0 && achados[ativo]) { ir(achados[ativo]); return; }
      const q = consulta();
      if (q.length < MINIMO) return;
      if (base.q === q && !voo) {
        if (base.lugares.length) ir(base.lugares[0]);
        else desenhar();
        return;
      }
      // Escolhe o primeiro assim que a resposta do texto atual chegar.
      enterPendente = true;
      if (falha && falha.q === q) { falha = null; pedir(q); }
      else agendar();
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
    const col = [...li.querySelectorAll(':scope > button')].indexOf(atual);
    const alvo = linhas[linhas.indexOf(li) + (e.key === 'ArrowDown' ? 1 : -1)];
    if (alvo) alvo.querySelectorAll(':scope > button')[col]?.focus();
    else if (e.key === 'ArrowUp') campo.focus();
  });

  // O primeiro clique depois de escolher seleciona tudo, com ou sem foco.
  // O clique roda depois do mouseup, então o navegador não desfaz a seleção.
  const selecionarTudo = () => campo.setSelectionRange(0, campo.value.length);
  campo.addEventListener('click', () => {
    if (escolhido && !escolhido.selecionado) { escolhido.selecionado = true; selecionarTudo(); }
    if (!aberta()) reabrir();
  });
  // Depois de escolher, a lista de onde veio a escolha reaparece.
  function reabrir() {
    if (escolhido && base.lugares.length && consulta() === escolhido.nome) {
      listaDaEscolha = true;
      desenhar();
      return;
    }
    agendar();
    desenhar();
  }
  campo.addEventListener('focus', () => {
    if (escolhido) selecionarTudo(); // foco pelo teclado
    if (!aberta()) reabrir();
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

  limpar.tabIndex = -1; // pelo teclado, Esc apaga
  atualizarLimpar();

  // Chamado quando o mapa termina de abrir.
  return function instalar(m, r) {
    map = m;
    rotas = r;
    if (pendente) { const [l, comoRota] = pendente; pendente = null; ir(l, comoRota); }
  };
})();
