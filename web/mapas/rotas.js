/* Cálculo e dados locais. Nenhuma coordenada sai deste servidor. */
function instalarRotas(map) {
  const el = id => document.getElementById('rota-' + id);
  const pontos = [null, null];
  const marcadores = [null, null];
  const vazio = {type:'FeatureCollection', features:[]};
  let escolhendo = null;
  let caminho = vazio;
  let pedido = null;
  let geracao = 0;
  let calculando = false;

  function informar(texto, erro = false) {
    el('estado').textContent = texto;
    el('estado').dataset.erro = String(erro);
  }
  function desenhar() {
    if (!map.getStyle()?.layers) return;
    if (!map.getSource('rota-local')) {
      map.addSource('rota-local', {type:'geojson', data:caminho});
      map.addLayer({id:'rota-contorno', type:'line', source:'rota-local',
        layout:{'line-cap':'round', 'line-join':'round'},
        paint:{'line-color':'#ffffff', 'line-width':8, 'line-opacity':.95}});
      map.addLayer({id:'rota-linha', type:'line', source:'rota-local',
        layout:{'line-cap':'round', 'line-join':'round'},
        paint:{'line-color':'#1268b4', 'line-width':5}});
    } else map.getSource('rota-local').setData(caminho);
  }
  function atualizarBotoes() {
    el('origem').disabled = false;
    el('destino').disabled = false;
    el('calcular').disabled = !pontos.every(Boolean) || calculando;
    el('calcular').textContent = calculando ? 'Calculando...' : 'Calcular caminho';
    el('inverter').disabled = !pontos.every(Boolean);
    el('centro').disabled = escolhendo === null;
    ['origem','destino'].forEach((nome, indice) => {
      el(nome).setAttribute('aria-pressed', String(escolhendo === indice));
      el(nome + '-valor').textContent = pontos[indice]
        ? pontos[indice][1].toFixed(5) + ', ' + pontos[indice][0].toFixed(5)
        : indice === 0 ? 'Não escolhida' : 'Não escolhido';
    });
    map.getCanvas().style.cursor = escolhendo === null ? '' : 'crosshair';
  }
  function invalidar() {
    geracao++;
    pedido?.abort();
    pedido = null;
    calculando = false;
    caminho = vazio;
    desenhar();
  }
  function escolher(indice) {
    escolhendo = indice;
    atualizarBotoes();
    informar('Clique no mapa para marcar ' + (indice === 0 ? 'a origem A.' : 'o destino B.') + ' Também pode mover o mapa e usar seu centro.');
  }
  function atualizarMarcador(indice) {
    marcadores[indice]?.remove();
    marcadores[indice] = null;
    if (!pontos[indice]) return;
    const etiqueta = document.createElement('div');
    etiqueta.className = 'rota-marcador' + (indice ? ' destino' : '');
    etiqueta.textContent = indice ? 'B' : 'A';
    etiqueta.title = indice ? 'Destino B. Arraste para mudar.' : 'Origem A. Arraste para mudar.';
    etiqueta.setAttribute('aria-label', etiqueta.title);
    const marcador = new maplibregl.Marker({element:etiqueta, draggable:true})
      .setLngLat(pontos[indice]).addTo(map);
    marcador.on('dragstart', () => { invalidar(); atualizarBotoes(); });
    marcador.on('dragend', () => {
      const p = marcador.getLngLat();
      pontos[indice] = [p.lng, p.lat];
      atualizarBotoes();
      informar('Ponto alterado. Clique em Calcular caminho.');
    });
    marcadores[indice] = marcador;
  }
  function marcar(ponto) {
    if (escolhendo === null) return;
    invalidar();
    const indice = escolhendo;
    pontos[indice] = [ponto.lng, ponto.lat];
    atualizarMarcador(indice);
    escolhendo = null;
    if (indice === 0 && !pontos[1]) escolher(1);
    else {
      atualizarBotoes();
      informar(pontos.every(Boolean) ? 'Pontos escolhidos. Clique em Calcular caminho.' : 'Escolha também a origem A.');
    }
  }
  el('origem').onclick = () => escolher(0);
  el('destino').onclick = () => escolher(1);
  el('centro').onclick = () => marcar(map.getCenter());
  map.on('click', evento => marcar(evento.lngLat));
  map.on('style.load', desenhar);
  el('painel').addEventListener('toggle', () => {
    if (!el('painel').open) { escolhendo = null; atualizarBotoes(); }
    requestAnimationFrame(() => map.resize());
  });
  el('perfil').onchange = () => {
    invalidar();
    atualizarBotoes();
    informar('Modo alterado. Escolha os pontos e calcule o caminho.');
  };
  el('inverter').onclick = () => {
    invalidar();
    pontos.reverse();
    [0,1].forEach(atualizarMarcador);
    escolhendo = null;
    atualizarBotoes();
    informar('Origem e destino invertidos. Clique em Calcular caminho.');
  };
  el('limpar').onclick = () => {
    invalidar();
    pontos.fill(null);
    [0,1].forEach(atualizarMarcador);
    escolhendo = null;
    atualizarBotoes();
    informar('Caminho limpo. Marque uma nova origem A.');
  };
  el('form').onsubmit = async evento => {
    evento.preventDefault();
    if (!pontos.every(Boolean) || calculando) return;
    invalidar();
    const atual = geracao;
    const controle = new AbortController();
    pedido = controle;
    calculando = true;
    escolhendo = null;
    atualizarBotoes();
    informar('Calculando caminho pelas ruas. Aguarde.');
    const limite = setTimeout(() => controle.abort(), 120000);
    try {
      const parametros = new URLSearchParams({
        lonlats:pontos.map(p => p.join(',')).join('|'),
        profile:el('perfil').value, alternativeidx:'0', format:'geojson'
      });
      const resposta = await fetch('./rotas?' + parametros, {signal:controle.signal, cache:'no-store'});
      if (!resposta.ok) {
        const mensagem = await resposta.text();
        if (/rd5|segment|coverage|cobertura|dados.*ausent/i.test(mensagem))
          throw new Error('Faltam dados de rotas nesta região. Escolha pontos dentro da cobertura instalada.');
        if (/timeout|time limit|memory|mem[oó]ria|running time/i.test(mensagem))
          throw new Error('O caminho excedeu o limite de cálculo. Tente um trajeto mais curto.');
        if ([502,503,504].includes(resposta.status))
          throw new Error('O serviço local de rotas está indisponível. Tente novamente em alguns instantes.');
        throw new Error('Não foi encontrado um caminho entre os pontos para este modo. Aproxime os pontos de ruas ou escolha outro modo.');
      }
      const dados = await resposta.json();
      const linha = dados.features?.find(f => f.geometry?.type === 'LineString');
      if (!linha || linha.geometry.coordinates.length < 2)
        throw new Error('O serviço não encontrou um caminho pelas ruas entre estes pontos.');
      if (atual !== geracao) return;
      caminho = {type:'FeatureCollection', features:[linha]};
      desenhar();
      const limites = linha.geometry.coordinates.reduce((b,p) => b.extend(p.slice(0,2)), new maplibregl.LngLatBounds());
      map.fitBounds(limites, {padding:55, maxZoom:16});
      const distancia = Number(linha.properties?.['track-length']);
      const segundos = Number(linha.properties?.['total-time']);
      const resumo = [];
      if (Number.isFinite(distancia) && distancia > 0)
        resumo.push(distancia < 1000 ? Math.round(distancia) + ' m' : (distancia / 1000).toLocaleString('pt-BR', {maximumFractionDigits:1}) + ' km');
      if (Number.isFinite(segundos) && segundos > 0) {
        const minutos = Math.max(1, Math.round(segundos / 60));
        resumo.push(minutos >= 60 ? Math.floor(minutos/60) + ' h ' + minutos%60 + ' min estimados' : minutos + ' min estimados');
      }
      informar('Caminho calculado offline' + (resumo.length ? ': ' + resumo.join(', ') : '') + '. Sem informação de trânsito em tempo real.');
    } catch (erro) {
      if (atual !== geracao) return;
      const mensagem = erro.name === 'AbortError'
        ? 'O cálculo demorou além do limite. Tente um trajeto mais curto.'
        : erro instanceof TypeError
        ? 'Não foi possível acessar o serviço local de rotas. Confira a conexão com o servidor da biblioteca.'
        : erro instanceof SyntaxError
        ? 'O serviço local não devolveu um caminho válido. Tente novamente.'
        : erro.message;
      informar(mensagem, true);
    } finally {
      clearTimeout(limite);
      if (atual === geracao) {
        calculando = false;
        pedido = null;
        atualizarBotoes();
      }
    }
  };
  atualizarBotoes();
  // Usado pela busca: marca origem (0) ou destino (1) num ponto achado.
  return {
    definir(indice, lngLat) {
      el('painel').open = true;
      escolhendo = indice;
      marcar(lngLat);
    }
  };
}
