/* Busca de endereços e lugares no índice local. Nada sai do servidor. */
function instalarBusca(map, rotas) {
  const campo = document.getElementById('busca-texto');
  const lista = document.getElementById('busca-lista');
  const form = document.getElementById('busca');
  let marcador = null, pedido = null, espera = null, achados = [], ativo = -1;

  fetch('./busca?q=teste', {cache: 'no-store'}).then(r => {
    if (r.status === 503) {
      campo.placeholder = 'Busca de endereços ainda não instalada';
    } else {
      campo.disabled = false;
    }
  }).catch(() => { campo.placeholder = 'Busca indisponível'; });

  function zoomDe(tipo) {
    if (tipo === 'Cidade') return 12;
    if (tipo === 'Vila' || tipo === 'Distrito' || tipo === 'Ilha') return 13;
    if (tipo === 'Bairro' || tipo === 'Povoado' || tipo === 'Localidade') return 14.5;
    if (tipo === 'Rodovia') return 13;
    if (tipo === 'Rua' || tipo === 'Avenida ou rua principal') return 16;
    return 17;
  }
  function fechar() { lista.hidden = true; ativo = -1; campo.setAttribute('aria-expanded', 'false'); }
  function ir(l) {
    const ponto = [l.lon, l.lat];
    marcador?.remove();
    const el = document.createElement('div');
    el.className = 'busca-marcador';
    el.title = l.nome;
    marcador = new maplibregl.Marker({element: el}).setLngLat(ponto).addTo(map);
    map.flyTo({center: ponto, zoom: zoomDe(l.tipo), speed: 1.6});
    campo.value = l.nome;
    fechar();
  }
  function mostrar() {
    lista.replaceChildren(...achados.map((l, i) => {
      const li = document.createElement('li');
      li.setAttribute('role', 'option');
      li.id = 'busca-op-' + i;
      li.setAttribute('aria-selected', String(i === ativo));
      const texto = document.createElement('button');
      texto.type = 'button';
      texto.className = 'busca-ir';
      const nome = document.createElement('b');
      nome.textContent = l.nome;
      const det = document.createElement('small');
      det.textContent = l.detalhe;
      texto.append(nome, det);
      texto.onclick = () => ir(l);
      const a = document.createElement('button');
      a.type = 'button'; a.textContent = 'A'; a.title = 'Usar como origem A';
      a.setAttribute('aria-label', 'Usar ' + l.nome + ' como origem A');
      a.onclick = () => { ir(l); rotas.definir(0, {lng: l.lon, lat: l.lat}); };
      const b = document.createElement('button');
      b.type = 'button'; b.textContent = 'B'; b.title = 'Usar como destino B';
      b.setAttribute('aria-label', 'Usar ' + l.nome + ' como destino B');
      b.onclick = () => { ir(l); rotas.definir(1, {lng: l.lon, lat: l.lat}); };
      li.append(texto, a, b);
      return li;
    }));
    if (!achados.length) {
      const li = document.createElement('li');
      li.className = 'busca-vazio';
      li.textContent = 'Nada encontrado. Tente o nome da rua com a cidade.';
      lista.append(li);
    }
    lista.hidden = false;
    campo.setAttribute('aria-expanded', 'true');
  }
  async function procurar() {
    const q = campo.value.trim();
    if (q.length < 2) { fechar(); return; }
    pedido?.abort();
    pedido = new AbortController();
    const c = map.getCenter();
    try {
      const r = await fetch('./busca?' + new URLSearchParams({q, lat: c.lat.toFixed(4), lon: c.lng.toFixed(4)}),
        {signal: pedido.signal, cache: 'no-store'});
      const d = await r.json();
      achados = d.lugares || [];
      ativo = -1;
      mostrar();
    } catch (e) {
      if (e.name !== 'AbortError') { achados = []; mostrar(); }
    }
  }
  campo.addEventListener('input', () => { clearTimeout(espera); espera = setTimeout(procurar, 250); });
  campo.addEventListener('keydown', e => {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      if (!achados.length) return;
      e.preventDefault();
      ativo = (ativo + (e.key === 'ArrowDown' ? 1 : -1) + achados.length) % achados.length;
      mostrar();
      campo.setAttribute('aria-activedescendant', 'busca-op-' + ativo);
    } else if (e.key === 'Escape') fechar();
  });
  form.addEventListener('submit', e => {
    e.preventDefault();
    if (achados.length) ir(achados[Math.max(0, ativo)]);
    else procurar();
  });
  document.addEventListener('click', e => { if (!form.contains(e.target)) fechar(); });

  // Vindo do buscador da página inicial: /mapas/?lat=..&lon=..&nome=..&tipo=..
  const pedidoInicial = new URLSearchParams(location.search);
  const lat = Number(pedidoInicial.get('lat')), lon = Number(pedidoInicial.get('lon'));
  if (pedidoInicial.has('lat') && Number.isFinite(lat) && Number.isFinite(lon) && Math.abs(lat) <= 90 && Math.abs(lon) <= 180) {
    const lugar = {lat, lon, nome: pedidoInicial.get('nome') || '', tipo: pedidoInicial.get('tipo') || ''};
    map.once('load', () => {
      if (map.getMaxBounds() && !map.getMaxBounds().contains([lon, lat])) map.setMaxBounds(null);
      ir(lugar);
    });
  }
}
