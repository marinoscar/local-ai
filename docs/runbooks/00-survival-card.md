<!--
Survival card TEMPLATE. Rendered per node by `aictl docs render` (docs/specs/aictl.md §4.22)
into <root>/docs/survival-card-<node-id>.{html,pdf}. Content and format: docs/specs/survival-portal.md §4.12.
Rules: ONE page (US Letter, 8.0 x 10.5 in safe area, also prints on A4), English left / Spanish right,
headings 18 pt, body 11 pt minimum, black on white. The renderer fails with LAI-CLI-015 if it overflows,
so keep every cell short. Placeholders are <UPPER_SNAKE_CASE>, filled from inventory.local.yaml and node state.
NEVER add the PIN, keys, IP addresses or passwords (the renderer refuses secret placeholders).
Button texts must match the Portal strings (survival-portal.md §4.9, btn.* and btn.*.sub).
Unprinted copy for reading only: the printable one is in the Portal, 6 Manuals -> Print the Survival card.
-->

| SURVIVAL CARD — Local AI computer <NODE_ID> | TARJETA DE SUPERVIVENCIA — computadora de IA local <NODE_ID> |
|---|---|
| **Start** 1) Plug in the charger. 2) Turn on and wait 2 minutes. 3) Log in as **<DESKTOP_USER>** (the password is not written here). 4) Open the **Local AI** app — or open a web browser and type `127.0.0.1:7070/portal`. 5) Press **Start survival mode**. **What you should see:** eight big buttons. | **Inicio** 1) Conecte el cargador. 2) Enciéndala y espere 2 minutos. 3) Inicie sesión como **<DESKTOP_USER>** (la contraseña no está escrita aquí). 4) Abra la aplicación **Local AI** — o abra un navegador web y escriba `127.0.0.1:7070/portal`. 5) Presione **Iniciar modo supervivencia**. **Lo que debería ver:** ocho botones grandes. |
| **The eight buttons** (click, or press the number) | **Los ocho botones** (haga clic, o presione el número) |
| **1 Chat** — Type questions | **1 Conversar** — Escriba preguntas |
| **2 Ask about a photo** — Take or choose a photo | **2 Preguntar por una foto** — Tome o elija una foto |
| **3 Talk** — Speak and listen | **3 Hablar** — Hable y escuche |
| **4 Library** — Wikipedia, medicine, how-to | **4 Biblioteca** — Wikipedia, medicina, guías |
| **5 Maps** — Offline map and place search | **5 Mapas** — Mapa sin internet y búsqueda de lugares |
| **6 Manuals** — How to use this computer | **6 Manuales** — Cómo usar esta computadora |
| **7 Status and battery** | **7 Estado y batería** |
| **8 Switch brain** — Choose the AI model | **8 Cambiar cerebro** — Elija el modelo de IA |
| **First aid** Press **F** on the home screen. This is not a doctor; if any phone works call **<EMERGENCY_NUMBER>**. | **Primeros auxilios** Presione **F** en la pantalla de inicio. Esto no es un médico; si algún teléfono funciona, llame al **<EMERGENCY_NUMBER>**. |
| **If the app doesn't open** Open a terminal (**Ctrl + Alt + T**) and type `aictl mode survival`, then open the browser address above. **Library only:** `127.0.0.1:8888/library/` | **Si la aplicación no abre** Abra una terminal (**Ctrl + Alt + T**) y escriba `aictl mode survival`; luego abra la dirección de arriba en el navegador. **Solo la biblioteca:** `127.0.0.1:8888/library/` |
| **Ark drive** Plug it in. Open `START-HERE.html` on the drive. | **Disco del arca (AI-ARK)** Conéctelo. Abra `START-HERE.html` en el disco. |
| **Battery** Keep it plugged in when you can. Closing the lid does not turn it off. | **Batería** Manténgala conectada cuando pueda. Cerrar la tapa no la apaga. |
| **Error codes** A code like `LAI-SVC-001` comes with a fix. More in the manual *Troubleshooting*. | **Códigos de error** Un código como `LAI-SVC-001` viene con una solución. Más en el manual *Solución de problemas* (Troubleshooting). |

Computer / Computadora **<NODE_ID>** · Brains / Cerebros: <BRAINS_INSTALLED> · Catalog / Catálogo <CATALOG_VERSION> · Printed / Impreso <RENDER_DATE>
