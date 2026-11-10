// Frontend del TP: login → buscar producto → ranking de proveedores → reservar
// stock. Solo le habla al api-gateway, con el JWT que devuelve el login.
"use strict";

const $ = (id) => document.getElementById(id);
const pesos = new Intl.NumberFormat("es-AR", { style: "currency", currency: "ARS" });

// El token vive en sessionStorage: se borra al cerrar la pestaña. Envuelto en
// try/catch porque en modo privado el acceso puede tirar.
const sesion = {
  leer() { try { return JSON.parse(sessionStorage.getItem("sesion")); } catch { return null; } },
  guardar(s) { try { sessionStorage.setItem("sesion", JSON.stringify(s)); } catch { /* sin storage */ } },
  borrar() { try { sessionStorage.removeItem("sesion"); } catch { /* sin storage */ } },
};

let actual = null;     // { token, usuario, vence }
let skuElegido = null;

// api llama al gateway. Los errores vienen como problem+json: se muestra su
// `detail`, y un 401 manda de vuelta al login (el token venció).
async function api(metodo, ruta, { cuerpo, headers = {} } = {}) {
  const h = { ...headers };
  if (actual) h.Authorization = "Bearer " + actual.token;
  if (cuerpo !== undefined) h["Content-Type"] = "application/json";
  const res = await fetch(ruta, { method: metodo, headers: h, body: cuerpo === undefined ? undefined : JSON.stringify(cuerpo) });
  const rid = res.headers.get("X-Request-ID");
  if (rid) $("request-id").textContent = "request " + rid;
  const tipo = res.headers.get("Content-Type") || "";
  const datos = tipo.includes("json") ? await res.json() : null;
  if (!res.ok) {
    if (res.status === 401 && actual) { salir(); }
    let msg = (datos && datos.detail) || res.statusText;
    if (res.status === 429) msg += ` (esperá ${res.headers.get("Retry-After") || "unos"} s)`;
    const err = new Error(msg);
    err.status = res.status;
    err.code = datos && datos.code;
    throw err;
  }
  return datos;
}

function mostrar(logueado) {
  $("vista-login").hidden = logueado;
  $("vista-app").hidden = !logueado;
  $("sesion").hidden = !logueado;
  if (logueado) $("usuario-actual").textContent = actual.usuario;
}

function salir() {
  actual = null;
  sesion.borrar();
  mostrar(false);
}

$("form-login").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const f = new FormData(ev.target);
  $("error-login").textContent = "";
  try {
    const r = await api("POST", "/api/v1/auth/login", { cuerpo: { usuario: f.get("usuario"), contrasena: f.get("contrasena") } });
    actual = { token: r.access_token, usuario: r.usuario, vence: Date.now() + r.expires_in * 1000 };
    sesion.guardar(actual);
    ev.target.reset();
    mostrar(true);
  } catch (e) {
    $("error-login").textContent = e.message;
  }
});

$("salir").addEventListener("click", salir);

// --- Búsqueda (GET /api/v1/productos/buscar, Meilisearch) ---
// Busca mientras se escribe, con una pausa de 250 ms para no mandar una request
// por tecla. `vuelta` descarta respuestas viejas que lleguen tarde. Si la
// búsqueda no está disponible, el texto se usa como SKU exacto: el ranking no
// depende del índice (ADR-006).
let resultados = [];
let elegido = null;   // { sku, nombre }
let pausa = null;
let vuelta = 0;

const NOTA_BUSQUEDA = $("nota-busqueda").textContent;

$("form-buscar").q.addEventListener("input", (ev) => {
  elegido = null;
  clearTimeout(pausa);
  const q = ev.target.value.trim();
  if (q.length < 2) { pintarResultados(null); return; }
  pausa = setTimeout(() => buscar(q), 250);
});

async function buscar(q) {
  const mia = ++vuelta;
  try {
    const r = await api("GET", `/api/v1/productos/buscar?q=${encodeURIComponent(q)}&tamanio=8`);
    if (mia !== vuelta) return;
    $("nota-busqueda").textContent = NOTA_BUSQUEDA;
    pintarResultados(r.resultados || []);
  } catch (e) {
    if (mia !== vuelta) return;
    pintarResultados(null);
    $("nota-busqueda").textContent = `La búsqueda no está disponible (${e.message}). Escribí el SKU exacto y tocá "Ver proveedores".`;
  }
}

function pintarResultados(lista) {
  resultados = lista || [];
  const ul = $("resultados");
  ul.replaceChildren();
  if (lista === null) { ul.hidden = true; return; }
  if (lista.length === 0) {
    const li = document.createElement("li");
    li.className = "vacio";
    li.textContent = "Sin resultados. Probá con otra palabra o con el SKU exacto.";
    ul.appendChild(li);
  }
  lista.forEach((p) => {
    const li = document.createElement("li");
    const b = document.createElement("button");
    b.type = "button";
    if (elegido && elegido.sku === p.sku) b.className = "elegido";
    const nombre = document.createElement("span");
    nombre.className = "nombre";
    nombre.textContent = p.nombre || p.sku;
    const precio = document.createElement("span");
    precio.className = "precio";
    precio.textContent = p.precio_min == null ? "sin stock" : "desde " + pesos.format(p.precio_min);
    const detalle = document.createElement("span");
    detalle.className = "detalle";
    const prov = p.proveedores_con_stock === 1 ? "1 proveedor" : `${p.proveedores_con_stock} proveedores`;
    detalle.textContent = [p.sku, p.categoria, prov + " con stock"].filter(Boolean).join(" · ");
    b.append(nombre, precio, detalle);
    b.addEventListener("click", () => elegir(p));
    li.appendChild(b);
    ul.appendChild(li);
  });
  ul.hidden = false;
}

function elegir(p) {
  elegido = { sku: p.sku, nombre: p.nombre || p.sku };
  $("form-buscar").q.value = elegido.nombre;
  pintarResultados(resultados);
  $("form-buscar").requestSubmit();
}

// skuDelFormulario resuelve qué producto pidió el usuario: el resultado que
// eligió, si no el primero de la lista, y si no hay lista, el texto tal cual.
function skuDelFormulario(texto) {
  if (elegido) return elegido.sku;
  if (resultados.length > 0) { elegir(resultados[0]); return null; }
  return texto;
}

$("form-buscar").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const f = new FormData(ev.target);
  const sku = skuDelFormulario(String(f.get("q")).trim());
  if (!sku) return;   // elegir() ya volvió a mandar el formulario
  const cantidad = Number(f.get("cantidad"));
  $("error-app").textContent = "";
  $("resultado-reserva").textContent = "";
  skuElegido = sku;

  // El ranking y el stock son independientes: se piden a la vez, y si uno
  // falla el otro se muestra igual.
  const [ranking, stock] = await Promise.allSettled([
    api("GET", `/api/v1/proveedores/recomendados?producto=${encodeURIComponent(sku)}&cantidad=${cantidad}&limite=10`),
    api("GET", `/api/v1/stock/${encodeURIComponent(sku)}`),
  ]);

  const errores = [];
  if (ranking.status === "fulfilled") pintarRanking(ranking.value);
  else { $("bloque-ranking").hidden = true; errores.push("Proveedores: " + ranking.reason.message); }
  if (stock.status === "fulfilled") pintarStock(stock.value);
  else { $("bloque-stock").hidden = true; errores.push("Stock: " + stock.reason.message); }
  $("error-app").textContent = errores.join(" · ");
});

function pintarRanking(ofertas) {
  const tbody = $("ranking");
  tbody.replaceChildren();
  ofertas.forEach((o, i) => {
    const tr = document.createElement("tr");
    if (i === 0) tr.className = "mejor";
    const celdas = [
      i + 1,
      o.proveedor_nombre,
      pesos.format(o.precio_unitario),
      o.descuento_aplicado ? o.descuento_pct + " %" : "—",
      pesos.format(o.flete),
      o.stock_disponible,
      pesos.format(o.costo_total),
    ];
    celdas.forEach((c, j) => {
      const td = document.createElement("td");
      td.textContent = c;
      if (j >= 2) td.className = "num";
      tr.appendChild(td);
    });
    tbody.appendChild(tr);
  });
  $("bloque-ranking").hidden = false;
}

function pintarStock(st) {
  $("st-disponible").textContent = st.disponible;
  $("st-reservado").textContent = st.reservado;
  $("st-minimo").textContent = st.minimo;
  $("bloque-stock").hidden = false;
}

$("form-reservar").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  if (!skuElegido) return;
  const cantidad = Number(new FormData(ev.target).get("cantidad"));
  const salida = $("resultado-reserva");
  salida.className = "";
  // Una Idempotency-Key por intención de reservar. El botón queda deshabilitado
  // mientras viaja la request, así que un doble clic no reserva dos veces.
  const clave = crypto.randomUUID();
  const boton = ev.target.querySelector("button");
  boton.disabled = true;
  try {
    const r = await api("POST", "/api/v1/stock/reservas", { cuerpo: { sku: skuElegido, cantidad }, headers: { "Idempotency-Key": clave } });
    salida.textContent = `Reserva ${r.id.slice(0, 8)} · ${r.cantidad} u. de ${r.sku} · ${r.estado}`;
    salida.className = "ok";
    pintarStock(await api("GET", `/api/v1/stock/${encodeURIComponent(skuElegido)}`));
  } catch (e) {
    salida.textContent = e.message;
    salida.className = "error";
  } finally {
    boton.disabled = false;
  }
});

// Arranque: sesión previa si sigue vigente.
const previa = sesion.leer();
if (previa && previa.vence > Date.now()) { actual = previa; mostrar(true); } else { mostrar(false); }
