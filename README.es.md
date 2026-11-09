# local-ai

🇬🇧 [English](README.md) · 🇪🇸 **Español**

**Ejecute IA local en un laboratorio casero y mantenga todo funcionando cuando no haya internet.**

Este repositorio es el manual de operación y el código fuente para dos tareas:

1. **Operar el laboratorio Local AI.** Los nodos con GPU ejecutan modelos abiertos (programación, chat, visión, agentes). Usted los observa y controla a todos desde un solo sitio web, `https://ai.marin.cr`, que además ofrece un único punto de acceso compatible con OpenAI en `https://ai.marin.cr/v1`.
2. **Ser un «arca» para el fin del mundo.** Cada nodo, sobre todo las laptops, puede funcionar **de forma totalmente independiente y sin internet**. Los modelos, una biblioteca del tamaño de Wikipedia, mapas, voz y un kit de reconstrucción ya están en el disco. Una persona sin conocimientos técnicos puede manejarlo desde la aplicación de escritorio **Local AI**.

Si un cambio hace que la tarea 2 sea menos confiable, ese cambio está mal, aunque mejore la tarea 1 ([AGENTS.md](AGENTS.md), en inglés).

---

## 🚨 En una emergencia

1. Conecte el cargador, encienda la computadora e inicie sesión.
2. Abra la aplicación **Local AI** y presione **Modo supervivencia**.
3. Si la aplicación no abre, abra un navegador web y vaya a `http://127.0.0.1:7070`.

Guía completa, imprimible, en inglés y español: [Manual del modo supervivencia](docs/runbooks/01-survival-mode.md). Una hoja para tener junto a la computadora: [Tarjeta de supervivencia](docs/runbooks/00-survival-card.md) (inglés y español lado a lado). No necesita internet ni el PIN de administrador para empezar.

> 🧪 **Estado:** el software para todo esto está escrito y probado en la integración continua, y el agente, el enrutador de modelos, el kit y los servicios de voz y biblioteca ya funcionan en un nodo real. La ruta de emergencia **todavía no se ha practicado de principio a fin en equipos reales**. Lea el [Estado](#10-estado-y-hoja-de-ruta) antes de depender de ella.

---

## Contenido
[1 Qué ofrece](#1-qué-ofrece) · [2 Cómo funciona](#2-cómo-funciona) · [3 Los tres modos](#3-los-tres-modos) · [4 El laboratorio hoy](#4-el-laboratorio-hoy) · [5 Inicio rápido según su rol](#5-inicio-rápido-según-su-rol) · [6 La línea de comandos](#6-la-línea-de-comandos) · [7 Uso de la API](#7-uso-de-la-api) · [8 Estructura del repositorio](#8-estructura-del-repositorio) · [9 Mapa de la documentación](#9-mapa-de-la-documentación) · [10 Estado y hoja de ruta](#10-estado-y-hoja-de-ruta) · [11 Compilar y contribuir](#11-compilar-y-contribuir) · [12 Nota de seguridad](#12-nota-de-seguridad) · [13 Licencia y créditos](#13-licencia-y-créditos)

## 1. Qué ofrece

Todo lo de la primera tabla funciona **sin internet**. Solo la segunda tabla lo necesita.

Leyenda de estado: ✅ construido, integrado y probado en la integración continua · 🧪 construido, y aún falta comprobarlo en equipos reales o en un simulacro (vea el [Estado](#10-estado-y-hoja-de-ruta)) · ⏳ construido, a la espera de un paso del propietario.

| Capacidad | Qué hace | Funciona sin internet | Estado |
|---|---|---|---|
| Chat y programación | Modelos de lenguaje locales mediante el enrutador de llama.cpp, trabajo con agentes, el chat web de llama.cpp | ✅ | 🧪 funcionando en ai-n3 |
| Visión | Hacer preguntas sobre una foto | ✅ | 🧪 |
| Voz de entrada y salida | Voz a texto (whisper.cpp) y texto a voz (Piper), en inglés y español | ✅ | 🧪 funcionando en ai-n3 |
| Biblioteca sin conexión | Kiwix: Wikipedia, contenido médico, guías prácticas, de reparación y de supervivencia | ✅ | 🧪 funcionando en ai-n3 |
| Mapas sin conexión | Visor de mapas con búsqueda de lugares | ✅ | 🧪 |
| Manuales | Estos manuales, convertidos a formato imprimible, guardados en el nodo | ✅ | 🧪 generados en ai-n3 |
| Kit de reconstrucción | Reconstruir sin internet el software de inferencia de un nodo, el agente y los paquetes | ✅ | 🧪 verificado en parte en ai-n3 |
| Portal de supervivencia y aplicación de escritorio | Pantalla de inicio con botones grandes para personas sin conocimientos técnicos, y la aplicación de escritorio Local AI que la rodea | ✅ | 🧪 |
| Simulacro mensual | Autoprueba que demuestra que el nodo sigue funcionando sin conexión | ✅ | 🧪 la primera ejecución pasó en ai-n3 |

| Solo con internet | Qué hace | Estado |
|---|---|---|
| Control en la nube | Ver el estado, cargar o cambiar modelos, llaves y la preparación del arca desde `ai.marin.cr` | ⏳ aplicación construida; el despliegue en producción es un paso del propietario |
| Puerta de enlace (gateway) | Una sola API compatible con OpenAI para todo el laboratorio en `/v1`: llaves, límites, uso, políticas de modelos, transmisión en vivo (streaming) y audio | ⏳ construida; el cambio en vivo es un paso del propietario |
| Sincronización del arca | Descarga nocturna y verificación de modelos y contenido; nunca borra la última copia buena | 🧪 verificada en ai-n3 |

## 2. Cómo funciona

```
 INTERNET (puede desaparecer en cualquier momento)
 ┌────────────────────────────────────────────────────────────┐
 │ VPS: nginx ─► ai.marin.cr ─► app en la nube (control + /v1)│
 │                    │            ▲                           │
 │                    │            │ solo HTTPS saliente       │
 │                    └─ Tailscale ┼──────────────┐            │
 └─────────────────────────────────┼──────────────┼───────────┘
                                   │              ▼
 ┌─────────────────────────────────┴───────────────────────────┐
 │ NODO (Ubuntu, GPU)                  funciona sin conexión   │
 │  aiagent ── única fuente de verdad en el nodo               │
 │   ├─ enrutador llama.cpp (modelos) ├─ Portal / interfaz     │
 │   ├─ kiwix (biblioteca)            ├─ mapas, manuales       │
 │   ├─ whisper + piper (voz)         └─ sync, simulacro, doctor│
 │  aictl (CLI)    Aplicación de escritorio Local AI (cascarón)│
 │  disco interno + unidad AI-ARK: modelos, biblioteca, mapas, kit│
 └─────────────────────────────────────────────────────────────┘
```

El diagrama detallado, la tabla de componentes y todos los nombres canónicos (rutas, puertos, unidades, tokens, URL) están en [overview.md](docs/architecture/overview.md) (en inglés). Las ideas clave:

- **El agente es la única fuente de verdad.** `aiagent` es un solo binario estático en Go en cada nodo; `aictl`, la interfaz local y la aplicación de escritorio son clientes suyos ([ADR-0001](docs/decisions/0001-agent-in-go.md)).
- **Los modelos se cambian con el modo enrutador de llama.cpp.** El agente genera los ajustes preestablecidos, así que un cambio no requiere reconstruir el servicio ([ADR-0002](docs/decisions/0002-llama-router-mode.md)).
- **El enlace con la nube es solo saliente.** Los nodos piden los comandos a la nube; la nube nunca se conecta hacia adentro ([ADR-0003](docs/decisions/0003-outbound-only-cloud-link.md)).
- **La aplicación de escritorio es un cascarón delgado** sobre la interfaz local del agente, y un navegador en `http://127.0.0.1:7070` siempre funciona como alternativa ([ADR-0004](docs/decisions/0004-desktop-thin-shell.md)).
- **El arca vive en dos lugares:** un subconjunto según el perfil en el disco interno, y todo en la unidad portátil AI-ARK ([ADR-0005](docs/decisions/0005-ark-storage.md)).
- **Los nodos sin conexión son independientes.** No hay malla en la red local; cada nodo debe poder vivir solo ([ADR-0006](docs/decisions/0006-standalone-offline.md)).
- **El repositorio es público y está depurado de datos reales.** Los valores reales del laboratorio viven solo en un archivo que git ignora ([ADR-0007](docs/decisions/0007-public-repo-redaction.md)).
- **La aplicación en la nube está construida sobre EnterpriseAppBase** ([ADR-0008](docs/decisions/0008-cloud-on-eab.md)).
- **Todo está optimizado para las personas.** Alguien que nunca ha usado una terminal debe poder hacer cada tarea común leyendo la pantalla y eligiendo una opción. Los comandos escritos son el camino rápido, nunca el único ([ADR-0009](docs/decisions/0009-optimized-for-humans.md)).

## 3. Los tres modos

Cada nodo está siempre en un solo modo, y se conserva al reiniciar. Detalles y máquina de estados: [modes.md](docs/architecture/modes.md) (en inglés).

- **`online` (en línea):** un miembro normal del laboratorio. Se alcanza por Tailscale, lo controla la nube y se sincroniza cada noche.
- **`offline` (sin conexión):** privado e independiente. Nada sale de la máquina; la IA local se puede usar por completo.
- **`survival` (supervivencia):** `offline` más todo lo necesario en una emergencia, con el Portal de supervivencia como pantalla de inicio. Cualquier persona puede activarlo sin PIN; para salir de él se necesita el PIN de administrador.

## 4. El laboratorio hoy

| Nodo | Clase de equipo | Backend | Perfil | Punto de acceso público |
|---|---|---|---|---|
| ai-n1 | MINISFORUM MS-S1 Max, 128 GB de memoria unificada | Vulkan | `max-128gb` | `https://ai-n1.marin.cr` |
| ai-n2 | MSI Stealth 17 Studio, RTX 4080 Laptop 12 GB (arranque dual con Windows) | CUDA | `cuda-12gb` | `https://ai-n2.marin.cr` |
| ai-n3 | HP ZBook Create G7, RTX 2070 Max-Q 8 GB | CUDA | `small-8gb` | `https://ai-n3.marin.cr` |

**ai-n3** es el primer nodo con el software nuevo: ejecuta el agente, el enrutador (Qwen 3.5 9B) y los servicios de voz y biblioteca, y fue la máquina usada para probar el kit de reconstrucción. **ai-n1** y **ai-n2** todavía ejecutan un `llama-server` administrado a mano; su migración se sigue en la épica E2. Modelos servidos, patrones de servicio y discrepancias conocidas: [lab-inventory.md](docs/architecture/lab-inventory.md) (en inglés). En este repositorio, las direcciones y los identificadores son marcadores de posición.

## 5. Inicio rápido según su rol

Cada punto es un manual escrito para alguien que nunca ha usado una terminal. Los manuales muestran los comandos; las especificaciones los definen. Los manuales están en inglés, salvo el del modo supervivencia y la tarjeta de supervivencia, que son bilingües.

- **Quiero chatear u obtener una llave de API** → [02 Chat y llaves de API](docs/runbooks/02-chat-and-api-keys.md)
  1. Abra la aplicación y use el chat, o
  2. ejecute `aictl key show` para obtener la llave del nodo.
- **Quiero cambiar un modelo** → [03 Cambiar un modelo](docs/runbooks/03-swap-a-model.md)
  1. Elija el modelo en la aplicación (o en la interfaz de la nube).
  2. Confirme; el agente recarga el enrutador.
- **Quiero preparar una unidad de arca** → [04 Unidad AI-ARK](docs/runbooks/04-ark-drive.md)
  1. Compre y formatee la unidad.
  2. Llénela con una sincronización y expúlsela de forma segura.
- **Quiero agregar un nodo o migrar uno existente** → [05 Agregar un nodo](docs/runbooks/05-add-a-node.md)
  1. Ejecute el instalador (`install-node.sh`).
  2. Inscríbalo en la nube y publique su punto de acceso.
- **Mi computadora murió; reconstruir sin conexión** → [06 Reconstruir un nodo sin conexión](docs/runbooks/06-rebuild-a-node-offline.md)
  1. Arranque desde una memoria USB con Ubuntu.
  2. Instale desde la unidad AI-ARK (`install-node.sh --from-ark`); no se necesita internet.
- **Algo no funciona** → [07 Solución de problemas](docs/runbooks/07-troubleshooting.md)
  1. Ejecute `aictl doctor`.
  2. Busque el código `LAI-…` que muestra.
- **Qué se ejecuta de forma programada** → [08 Sincronización programada](docs/runbooks/08-scheduled-sync.md)
- **Una laptop debe seguir encendida con la tapa cerrada** → [09 Laptop como servidor](docs/runbooks/09-laptop-as-server.md)
- **Administro el laboratorio** → [10 Operación del laboratorio](docs/runbooks/10-lab-operations.md)
- **Quiero desplegar o actualizar la aplicación de la nube en el VPS** → [README de laictl](apps/cloud/apps/cli/README.md), luego [10 Operación del laboratorio, parte 9](docs/runbooks/10-lab-operations.md#part-9-deploying-and-updating-the-cloud)
  1. Instale `laictl` con el instalador de una línea.
  2. `laictl deploy install`, y más adelante `laictl deploy update`.
- **Quiero hacer una prueba de carga a la puerta de enlace** → [11 Prueba de carga de la puerta de enlace](docs/runbooks/11-gateway-load-test.md)

¿Es nuevo y no sabe por dónde empezar? Comience en [00 Empiece aquí](docs/runbooks/00-START-HERE.md). Todos los manuales: [índice de manuales](docs/runbooks/README.md).

## 6. La línea de comandos

Hay dos líneas de comandos. Hacen trabajos distintos y se instalan de forma distinta:

| CLI | Dónde se ejecuta | Qué hace | Cómo se instala |
|---|---|---|---|
| `aictl` | en cada nodo | Opera un nodo: modelos, modos, claves, arca, diagnóstico | Viene con el nodo. Lo instala `install-node.sh` de una versión `node-v*` ([05 Agregar un nodo, parte 3](docs/runbooks/05-add-a-node.md#part-3-run-the-installer)) |
| `laictl` | en el VPS, o en su propia computadora | Instala, actualiza y revisa la aplicación de la nube (`laictl deploy doctor\|install\|update`); ejecuta el stack-agent del VPS; llama a la API de la nube | `curl -fsSL https://raw.githubusercontent.com/marinoscar/local-ai/deploy/cloud/install.sh \| bash` ([README de laictl](apps/cloud/apps/cli/README.md)) |

Para agregar un nodo, descargue el instalador de una versión, verifíquelo y ejecútelo en la nueva máquina Ubuntu:

```bash
curl -fsSL https://github.com/marinoscar/local-ai/releases/download/node-v<version>/install-node.sh -o install-node.sh
sha256sum install-node.sh                        # debe coincidir con SHA256SUMS de la versión (o con el asistente Add node de la nube)
sudo bash install-node.sh                        # pregunta el nombre del nodo, el origen y el enlace con la nube; primero muestra una vista previa
```

O lo mismo con opciones escritas: `sudo bash install-node.sh --node-id ai-n4 --cloud-url https://ai.marin.cr --enroll` (agregue `--dry-run` para la vista previa).

Sin internet, desde la unidad AI-ARK: `sudo bash /media/<usuario>/AI-ARK/localai/kit/installers/install-node.sh --from-ark /media/<usuario>/AI-ARK`. El procedimiento completo está en [05 Agregar un nodo](docs/runbooks/05-add-a-node.md); cada opción está en [installer.md](docs/specs/installer.md).

### `aictl`

`aictl` es la línea de comandos del agente del nodo. **Escriba `aictl` y presione Enter**: se abre un menú y usted elige con las flechas (o el número) y Enter. No hay nada que memorizar: cada tarea común está ahí, y cada una muestra el comando escrito que ejecutó, así aprende el camino rápido mientras la usa ([aictl.md §3.6](docs/specs/aictl.md#36-full-screen-menu-aictl-with-no-arguments)). Los scripts y las tuberías nunca ven el menú. Ejecute `aictl help` para ver la lista completa de comandos escritos, y `aictl help <comando>` para uno solo. Los que más usará:

| Comando | Qué hace |
|---|---|
| `aictl status` | Estado de este nodo: modo, modelo, servicios, arca, nube |
| `aictl doctor` | Revisiones de salud; cada problema se explica con un código `LAI-…` y una solución |
| `aictl mode [online\|offline\|survival]` | Mostrar o cambiar el modo |
| `aictl models` / `aictl load <modelo>` | Listar los modelos y cargar uno |
| `aictl key show` / `aictl key copy` | La llave de API del nodo y las direcciones; copiar la llave sin mostrarla |
| `aictl ark status` / `aictl ark sync` | Qué tan completo está el contenido sin conexión; descargarlo y verificarlo |
| `aictl kit status` / `aictl kit verify` | Si el kit de reconstrucción está presente, firmado y sin daños |
| `aictl rebuild-llama` / `rebuild-whisper` / `rebuild-agent` | Reconstruir un programa desde el código fuente fijado, incluso sin internet |
| `aictl drill` | La autoprueba sin conexión (el simulacro) |
| `aictl docs render` | Generar los manuales y la tarjeta de supervivencia para uso sin conexión |

El contrato completo está en [aictl.md](docs/specs/aictl.md) (en inglés).

## 7. Uso de la API

A través de la puerta de enlace del laboratorio, con una llave de la puerta de enlace (`$LAI_KEY` contiene una llave que empieza con `lai_`):

```bash
curl -s https://ai.marin.cr/v1/models -H "Authorization: Bearer $LAI_KEY"
```

En un nodo, con la llave propia del nodo (`$LLAMA_API_KEY`), incluso sin internet:

```bash
curl -s http://127.0.0.1:8080/v1/models -H "Authorization: Bearer $LLAMA_API_KEY"
```

Ambas son compatibles con OpenAI, así que sirve cualquier cliente de OpenAI. Cómo configurar OpenCode y Open WebUI, Python y la transmisión en vivo: [02 Chat y llaves de API](docs/runbooks/02-chat-and-api-keys.md). El diseño, los límites y los códigos de error de la puerta de enlace: [gateway.md](docs/specs/gateway.md). Puertos y URL: [overview.md §3](docs/architecture/overview.md#3-canonical-names-single-source-of-truth).

## 8. Estructura del repositorio

```
AGENTS.md / CLAUDE.md       reglas para cada persona y agente de programación
inventory.example.yaml      marcadores de posición para los valores reales (archivo real: inventory.local.yaml, ignorado por git)
docs/                       toda la documentación (vea el mapa más abajo)
catalog/                    models.yaml, ark.yaml y perfiles de nodo, como datos
agent/                      módulo Go: el demonio aiagent, la CLI aictl y la interfaz local integrada (agent/ui)
installers/                 install-node.sh, con y sin internet (--from-ark)
packages/ui/                componentes React compartidos y recursos de marca
apps/cloud/                 ai.marin.cr: interfaz de control y puerta de enlace /v1, sobre EnterpriseAppBase
apps/desktop/               la aplicación de escritorio Local AI (Electron, deb y AppImage)
scripts/                    herramientas del repositorio: depuración de datos, enlaces, códigos del doctor, ganchos, lanzamientos y firmas
```

## 9. Mapa de la documentación

| Carpeta | Contenido |
|---|---|
| [docs/runbooks/](docs/runbooks/README.md) | Procedimientos paso a paso para operadores, en lenguaje sencillo |
| [docs/architecture/](docs/architecture/overview.md) | Visión general (**nombres canónicos**), [modos](docs/architecture/modes.md), [modelo de seguridad](docs/architecture/security-model.md), [inventario del laboratorio](docs/architecture/lab-inventory.md), [firma de lanzamientos](docs/architecture/release-signing.md) |
| [docs/specs/](docs/README.md#specs) | Un contrato por componente, más el [registro de códigos del doctor](docs/specs/doctor-codes.md) |
| [docs/decisions/](docs/decisions/README.md) | Registros de decisiones de arquitectura (ADR) |
| [docs/source/](docs/README.md) | Archivo depurado de los documentos originales. Solo historia |
| [docs/ROADMAP.md](docs/ROADMAP.md) | Épicas, mapa de tareas, grafo de dependencias |

La tabla componente → especificación → épica se mantiene en [docs/README.md](docs/README.md#specs) para que no se desfase de esta página. La documentación técnica está en inglés; el manual del modo supervivencia y la tarjeta de supervivencia están en inglés y español.

## 10. Estado y hoja de ruta

**El software de todas las épicas está escrito, integrado y probado en la integración continua.** Lo que falta es comprobarlo en equipos reales, los pasos que solo puede dar el propietario y los simulacros. No se afirma que nada de lo anterior funcione en una emergencia hasta que su simulacro haya pasado ([E9](https://github.com/marinoscar/local-ai/issues/10)).

| Fase | Objetivo | Épicas | Estado |
|---|---|---|---|
| 0 | Base: repositorio, controles de CI, documentación | [E0 #1](https://github.com/marinoscar/local-ai/issues/1) | Controles listos; sigue abierta la creación del inventario privado en las máquinas del propietario |
| 1 | Agente del nodo y arca, totalmente sin conexión | [E1 #2](https://github.com/marinoscar/local-ai/issues/2) agente y CLI · [E2 #3](https://github.com/marinoscar/local-ai/issues/3) modelos y enrutador · [E3 #4](https://github.com/marinoscar/local-ai/issues/4) sincronización del arca · [E4 #5](https://github.com/marinoscar/local-ai/issues/5) servicios complementarios y Portal · [E5 #6](https://github.com/marinoscar/local-ai/issues/6) instalador y kit | E1 y E3 completas. E2: el enrutador y la migración de ai-n3 están hechos; siguen abiertas la migración de ai-n1 y ai-n2 y la lista de modelos fijados. E4: queda un punto abierto (selección del modelo de voz). E5: solo falta fijar `kit-desktop`, más las pruebas de instalación y reconstrucción sin conexión en equipos reales |
| 2 | Nube y puerta de enlace | [E6 #7](https://github.com/marinoscar/local-ai/issues/7) aplicación en la nube · [E7 #8](https://github.com/marinoscar/local-ai/issues/8) puerta de enlace | Ambas construidas. Abiertos, solo del propietario: el despliegue en producción y el DNS (#92), el agente del VPS (#93), el cambio de Open WebUI (#146) y la ejecución de la prueba de carga (#147). Pasar los comandos de los nodos a trabajos de EnterpriseAppBase (#94) espera dos cambios externos |
| 3 | Escritorio | [E8 #9](https://github.com/marinoscar/local-ai/issues/9) aplicación de escritorio | Construida. Abierto: el primer lanzamiento firmado (#129) |
| Todo el proyecto | Simulacros, control de calidad, verificación de manuales | [E9 #10](https://github.com/marinoscar/local-ai/issues/10) | Casi todo abierto: verificación de manuales y simulacros |

Mapa completo de tareas, oleadas y ruta crítica: [ROADMAP.md](docs/ROADMAP.md). Estado en vivo: [GitHub Issues](https://github.com/marinoscar/local-ai/issues). Actualice esta sección cada vez que se entregue una épica.

## 11. Compilar y contribuir

- **Primero el issue.** Todo cambio se registra en un issue de GitHub dentro de una épica. Un issue = una rama `<tipo>/<n.º de issue>-<descripción>` = un PR que dice `Closes #<issue>`. Los commits siguen Conventional Commits (`feat(agent): …`).
- **Depuración de datos.** El repositorio es público. Ejecute `scripts/install-hooks.sh` una vez por clon; bloquea los commits que filtren valores del laboratorio. Revise cuando quiera con `scripts/check-redaction.sh`. Detalles en [AGENTS.md](AGENTS.md#local-setup-once-per-clone).
- **Otras revisiones antes de subir cambios:** `scripts/check-links.sh` (todos los enlaces y anclas se resuelven), `scripts/check-doctor-codes.sh` (los códigos de error coinciden con sus especificaciones) y `scripts/check-runbooks.sh` (cada comando, opción, código y ruta que muestra un manual existe). La CI ejecuta todas.
- **Compilar y probar cada parte:**

  | Parte | Comandos |
  |---|---|
  | Agente y CLI (Go) | `scripts/agent-ci.sh all` (análisis estático, pruebas con detector de carreras, staticcheck, verificación de dependencias, compilaciones estáticas amd64 y arm64) |
  | Interfaz local | `cd agent/ui && npm ci && npm run build` |
  | Aplicación en la nube y puerta de enlace | `cd apps/cloud && npm ci && npm run typecheck && npm test` (Node 24) |
  | Aplicación de escritorio | `cd apps/desktop && npm ci && npm run typecheck && npm test`; `npm run dist` genera el deb y el AppImage |

- **Tecnologías** (decididas en los ADR):

  | Componente | Tecnología |
  |---|---|
  | Agente y CLI | Go, un binario estático para linux/amd64 y linux/arm64 |
  | Interfaz local y Portal de supervivencia | React y Vite, integrados en el agente, sin recursos de red |
  | Aplicación en la nube y puerta de enlace | EnterpriseAppBase: NestJS, Prisma, PostgreSQL, React |
  | Aplicación de escritorio | Electron, un cascarón delgado |
  | Inferencia | `llama-server` de llama.cpp en modo enrutador |

- **Agentes de programación:** lean [AGENTS.md](AGENTS.md), luego su issue y luego las secciones de la especificación que enlaza. Si la especificación está equivocada, corríjanla en el mismo PR.

## 12. Nota de seguridad

Este repositorio es **público**. Contiene marcadores de posición como `<TAILNET_IP_AI_N1>` y `<VPS_PUBLIC_IP>`, nunca direcciones, llaves, tokens ni identificadores de zona reales. Los valores reales viven solo en `inventory.local.yaml`, que git ignora y que existe en las máquinas del propietario y en la unidad del arca ([inventory.example.yaml](inventory.example.yaml), [modelo de seguridad §5](docs/architecture/security-model.md#5-redaction-public-repo)).

**Si encuentra un valor filtrado:** no lo pegue en un issue ni en un PR. Abra un reporte privado con **Security → Report a vulnerability** de GitHub en este repositorio e indique el archivo y la línea. El propietario cambiará el valor y limpiará el historial.

## 13. Licencia y créditos

**Este repositorio todavía no tiene un archivo de licencia.** El propietario no ha elegido una. Mientras no se agregue, el autor se reserva todos los derechos. La aplicación en la nube de `apps/cloud/` se genera a partir de EnterpriseAppBase y conserva el archivo de licencia de ese proyecto.

Local AI se apoya en estos proyectos. Revise la licencia de cada uno antes de redistribuir cualquier cosa, sobre todo el **contenido** que va en una unidad de arca:

| Proyecto | Para qué se usa | Licencia (origen) |
|---|---|---|
| [llama.cpp](https://github.com/ggml-org/llama.cpp) | Inferencia de modelos de lenguaje y chat web | MIT |
| [whisper.cpp](https://github.com/ggml-org/whisper.cpp) | Voz a texto | MIT |
| [Piper](https://github.com/rhasspy/piper) | Texto a voz (cada voz tiene su propia licencia) | MIT |
| [Kiwix](https://kiwix.org) | Biblioteca sin conexión | GPL-3.0 (kiwix-tools) |
| [Protomaps](https://protomaps.com) y [MapLibre](https://maplibre.org) | Mosaicos de mapas y visor sin conexión | BSD-3-Clause |
| [OpenStreetMap](https://www.openstreetmap.org/copyright) | Datos de mapas | ODbL 1.0 |
| [GeoNames](https://www.geonames.org) | Búsqueda de lugares sin conexión | CC BY 4.0 |
| Wikipedia y otro contenido de Kiwix | Contenido de la biblioteca | en su mayoría CC BY-SA 4.0, varía según cada ZIM |
| EnterpriseAppBase | Base de la aplicación en la nube | vea su repositorio |

Los archivos de modelos también tienen sus propias licencias; el catálogo las registra por modelo.
