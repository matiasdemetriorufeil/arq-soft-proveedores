"""Resume una carpeta de resultados de medir.sh en tablas Markdown.

Uso: python tests/carga/resumir.py tests/carga/resultados/AAAA-MM-DD

Por corrida: tasa atendida, latencias, errores por código, iteraciones que k6
no llegó a lanzar y VU usados (de k6-*.json), y CPU media y memoria máxima de
cada contenedor mientras k6 estaba corriendo (de stats-*.csv).
"""

import csv
import json
import re
import sys
from pathlib import Path

CONTENEDORES = ["api-gateway", "proveedores-service-1", "proveedores-service-2", "postgres", "jaeger", "k6-carga"]


def mib(texto):
    m = re.match(r"([\d.]+)\s*([KMG]i?B)", texto.strip())
    if not m:
        return 0.0
    valor, unidad = float(m.group(1)), m.group(2)
    return valor * {"KiB": 1 / 1024, "MiB": 1, "GiB": 1024, "kB": 1 / 1000, "MB": 1, "GB": 1000}.get(unidad, 1)


def nombre_corto(nombre):
    nombre = nombre.replace("arq-soft-proveedores-", "")
    return re.sub(r"-1$", "", nombre)


def recursos(path):
    """CPU media y memoria máxima por contenedor, solo en las muestras con k6 vivo."""
    por_hora = {}
    for fila in csv.reader(path.open(encoding="utf-8")):
        if len(fila) < 4:
            continue
        hora, nombre, cpu, mem = fila[0], nombre_corto(fila[1]), fila[2], fila[3].split("/")[0]
        por_hora.setdefault(hora, {})[nombre] = (float(cpu.rstrip("%") or 0), mib(mem))
    # Las corridas viejas no registraban el contenedor de k6: ahí se usan todas.
    muestras = [m for m in por_hora.values() if "k6-carga" in m] or list(por_hora.values())
    out = {}
    for c in CONTENEDORES:
        valores = [m[c] for m in muestras if c in m]
        if valores:
            out[c] = (sum(v[0] for v in valores) / len(valores), max(v[1] for v in valores))
    return out, len(muestras)


def fmt_ms(v):
    return f"{v / 1000:.2f} s" if v >= 1000 else f"{v:.1f} ms"


def main(carpeta):
    carpeta = Path(carpeta)
    corridas = sorted(
        carpeta.glob("k6-*.json"),
        key=lambda p: (p.stem[3:].isdigit(), int(p.stem[3:]) if p.stem[3:].isdigit() else 0, p.stem),
    )
    print("| Corrida | Atendidas (req/s) | p50 | p95 | p99 | máx | Errores | Por código | No lanzadas | VU máx |")
    print("|---|---:|---:|---:|---:|---:|---:|---|---:|---:|")
    for p in corridas:
        m = json.load(p.open(encoding="utf-8"))["metrics"]
        d = m["http_req_duration"]
        codigos = {
            k.replace("respuestas_", ""): int(v["count"])
            for k, v in m.items()
            if k.startswith("respuestas_") and k != "respuestas_200" and v.get("count")
        }
        cod = ", ".join(f"{k}: {v}" for k, v in codigos.items()) or "—"
        print(
            f"| {p.stem[3:]} | {m['http_reqs']['rate']:.0f} | {fmt_ms(d['med'])} | {fmt_ms(d['p(95)'])} "
            f"| {fmt_ms(d['p(99)'])} | {fmt_ms(d['max'])} | {m['http_req_failed']['value'] * 100:.3f} % | {cod} "
            f"| {int(m.get('dropped_iterations', {}).get('count', 0))} | {int(m['vus_max']['max'])} |"
        )

    print()
    print("CPU media (% de un núcleo) / memoria máxima (MiB) mientras corría k6:")
    print()
    print("| Corrida | " + " | ".join(CONTENEDORES) + " | muestras |")
    print("|---|" + "---:|" * (len(CONTENEDORES) + 1))
    for p in corridas:
        stats = carpeta / p.name.replace("k6-", "stats-").replace(".json", ".csv")
        if not stats.exists():
            continue
        r, n = recursos(stats)
        celdas = [f"{r[c][0]:.0f} % / {r[c][1]:.0f}" if c in r else "—" for c in CONTENEDORES]
        print(f"| {p.stem[3:]} | " + " | ".join(celdas) + f" | {n} |")


if __name__ == "__main__":
    main(sys.argv[1] if len(sys.argv) > 1 else "tests/carga/resultados")
