# 12. Make a song, and switch what a computer does

Use this when you want a node to **make music** from your lyrics, or to switch a computer between jobs such as **Coding** and **Music**.

> **Before you start**
> - You need: a computer with an NVIDIA graphics card that has the **Music** workload (ask the lab owner, or look for a **Music** button; Part 1 shows where).
> - For the website way: internet, the node in **online** mode, and an account on `https://ai.marin.cr` that may use music.
> - Deleting a song needs the **admin PIN** on the node or in the desktop app.
> - **Time:** switching to Music takes about 1 to 2 minutes; one 1-minute song takes a few minutes. These times are approximately; to be confirmed by #505.

> 🛠 Verify when implemented (Epic E11: #509 workloads, #510 music API, #512 local page, #514 website page, #515 desktop app).

**Words used on this page**
- **Workload:** what the computer is set up to do right now, for example **Coding**, **Chat and photos** or **Music**. The graphics card has room for only one big job at a time, so choosing **Music** stops the coding AI, and choosing **Coding** stops the music maker.
- **Lyrics:** the words of the song. Parts of the song are marked with tags in square brackets, such as `[verse]` and `[chorus]`.
- **Style:** a few words that describe the sound, for example `folk, acoustic guitar, warm`.
- **Sample:** a short piece of audio you add so the song follows it. **Sounds like this** borrows its style. **Same song, my lyrics** keeps its melody and sings your words over it.
- **Version:** the computer can make up to 4 different songs from the same lyrics at once.

**Rights.** Use only audio and lyrics that you wrote, own, or have permission to use.

**Survival mode.** Music stops by itself when the computer enters **Survival** mode, so the emergency AI gets the graphics card. Songs you already made are kept. Music cannot be started in Survival mode.

---

## Part 1. Switch a computer to Music, and back to Coding

Choose **one** of the four ways below. They all do the same thing.

### From the website (ai.marin.cr)

1. Open `https://ai.marin.cr` → **Lab** → press **Open** on the node.
   - **What you should see:** at the top of the node page, a line "This computer is set for: **Coding**" and one button per workload: **Coding**, **Chat and photos**, **Music**, and so on. The current one looks pressed.
   - **If it doesn't work:** the buttons are missing when the node is in **Offline** or **Survival** mode, or was not seen for a while: use one of the other ways, on the computer itself. A greyed-out button says why (for example "does not fit"): see [07-troubleshooting.md](07-troubleshooting.md#music-or-switching-doesnt-work).
2. Press **Music**.
   - **What you should see:** "Running command: Switch to Music", then after about 1 to 2 minutes (approximately; to be confirmed by #505) the **Music** button looks pressed.
   - **If it doesn't work:** the page shows a code and its fix. `LAI-LLM-052`: the switch failed and the previous AI was started again; try once more. `LAI-LLM-051`: Music does not fit this computer's graphics card. `LAI-LLM-043`: the music program is not installed; press **Install what's missing** on the node page. More in [07-troubleshooting.md](07-troubleshooting.md#music-or-switching-doesnt-work).
3. When you are done with music, press **Coding**.
   - **What you should see:** the **Coding** button looks pressed again after about a minute.
   - **If it doesn't work:** same codes as step 2.

### From the desktop app

4. Open the **Local AI** app. On **This computer**, find the box "What this computer does".
   - **What you should see:** the same buttons as on the website, with the current one marked.
   - **If it doesn't work:** if the app does not open, use a web browser at `http://127.0.0.1:7070` (it is the same screen).
5. Press **Music**. When you are done, press **Coding**.
   - **What you should see:** a progress bar ("Stopping Coding", "Starting Music"), then the new choice is marked.
   - **If it doesn't work:** the app shows the code and fix, as in step 2. `LAI-AGT-022`: something else is being loaded; wait a minute and press again.

### From the computer's own screen (any web browser)

6. On the node, open a web browser and go to `http://127.0.0.1:7070/music/`.
   - **What you should see:** the **Music** page. Its first line says either "Music is ready" or "This computer is set for Coding" with a button **Switch to Music**.
   - **If it doesn't work:** if the page asks you to sign in, type the code shown by `aictl ui-code` (see [02-chat-and-api-keys.md](02-chat-and-api-keys.md)). If the page does not load at all, see [07-troubleshooting.md](07-troubleshooting.md#music-or-switching-doesnt-work).
7. Press **Switch to Music**.
   - **What you should see:** "Music is starting (about a minute)", then "Music is ready".
   - **If it doesn't work:** `LAI-SVC-150`: the music program is missing; press **Install what's missing**. `LAI-SVC-159`: the graphics driver is too old for music; run the doctor (Part 4).

### From the terminal menu (nothing to type but `aictl`)

8. Open a terminal (**Ctrl + Alt + T**), type `aictl` and press **Enter**.
   - **What you should see:** the menu, with the state of the computer at the top.
   - **If it doesn't work:** `LAI-CLI-001` means the agent is not running; see [07-troubleshooting.md](07-troubleshooting.md#music-or-switching-doesnt-work).
9. Press **4** (AI models), then choose **Switch what this computer does**.
   - **What you should see:** the list of workloads with their names, for example `Coding (now)`, `Chat and photos`, `Music`. The screen also shows the typed command, `aictl workloads`.
   - **If it doesn't work:** a workload marked `does not fit` or `missing: …` cannot be chosen; see [07-troubleshooting.md](07-troubleshooting.md#music-or-switching-doesnt-work).
10. Choose **Music** with the arrow keys and press **Enter**.
    - **What you should see:** `… Stopping Qwen 3.5 9B to make room`, `… Starting ACE-Step 1.5 (music)` with a bar, then `✓ This computer now makes music.` The typed command was `aictl workloads activate music`.
    - **If it doesn't work:** the line starting with `✗` gives the code and a fix (`LAI-LLM-052`, `LAI-LLM-051`, `LAI-LLM-043`). To go back to coding, do the same and choose **Coding**.

## Part 2. Make a song

The song is made on the node. You can ask for it on the node itself (the **Music** page, the desktop app's **Music** button) or from the website (`https://ai.marin.cr/music`). The steps are the same; the differences are noted.

11. Open the **Music** page: in the desktop app press **Music** in the top bar; on the node's browser go to `http://127.0.0.1:7070/music/`; on the website open `https://ai.marin.cr/music` and choose the node at the top right.
    - **What you should see:** the first line says "Music is ready" (on the website: the node shows `Music ✔`), and a form with **Lyrics**, **Style**, **Length**, **Language**, **How many versions**.
    - **If it doesn't work:** `LAI-SVC-151` means music is not running on that computer: press **Switch to Music** (Part 1).
12. Type your lyrics in **Lyrics**. Put each part under a tag. An invented example:

    ```
    [verse]
    Morning light on the kitchen floor
    Coffee steam and an open door
    [chorus]
    We keep on walking, slow and free
    Down to the river, you and me
    ```

    - **What you should see:** the text in the box. Short is better: a few verses and a chorus.
    - **If it doesn't work:** `LAI-SVC-154` with "lyrics too long": the limit is about 8,000 letters; remove some verses.
13. Type a **Style**, for example `folk, acoustic guitar, warm, female voice`. Choose the **Length** (for example 1:00), the **Language** of the lyrics, and **How many versions**: choose **3** or **4**, then keep the one you like best.
    - **What you should see:** your choices in the form.
    - **If it doesn't work:** the length goes from 10 seconds to 4 minutes; the slider does not go further.
14. Optional: press **Add a sample** and choose an audio file (WAV, MP3, FLAC or Ogg). Use **15 to 30 seconds** of audio you have the rights to. Then choose:
    - **Sounds like this**: a new song in the style of the sample.
    - **Same song, my lyrics**: the sample's melody, sung with your lyrics.

    Under **More options**, **How closely** sets how much the song follows the sample.
    - **What you should see:** the file name next to **Add a sample**, and one of the two choices marked.
    - **If it doesn't work:** `LAI-SVC-154`: the file is not audio, is bigger than 20 MB, or longer than 60 seconds. Cut it to 15 to 30 seconds and try again.
15. Press **Make the song**.
    - **What you should see:** a progress bar under **Requests** (on the website: "running"), then the new songs at the top of **My songs**. A 1-minute song takes a few minutes (approximately; to be confirmed by #505).
    - **If it doesn't work:** `LAI-SVC-153`: four songs are already waiting; wait for one to finish. `LAI-SVC-155`: the disk is almost full; delete old songs (step 18). `LAI-SVC-152`: the music program stopped in the middle; press **Switch to Music** again and retry. On the website also `LAI-SVC-156` (the sample did not reach the node: send it again) and `LAI-SVC-157` (the song could not be sent back; it is kept on the node). To stop a song, press **Cancel**; it shows `LAI-SVC-158`, which is not an error.
16. Press **▶** on a song to listen.
    - **What you should see:** the song plays. The title is the first line of your lyrics.
    - **If it doesn't work:** turn up the computer's volume; try another version of the song.
17. Press **Download** to save the song.
    - **What you should see:** a `.wav` file in your **Downloads** folder (in the desktop app: in your **Music** or **Downloads** folder).
    - **If it doesn't work:** check that the disk of your computer is not full.
18. To delete a song you don't want, press **Delete** and confirm. On the node and in the desktop app, type the **admin PIN** when asked.
    - **What you should see:** the song disappears from **My songs**. Deleting a song on the website deletes only the website's copy; the copy on the node stays until you delete it there.
    - **If it doesn't work:** a wrong PIN shows `LAI-SEC-002`; try again. A song still being made cannot be deleted; wait or press **Cancel** first.

## Part 3. Tips

- Make 3 or 4 versions and keep the best one: each version is different.
- A sample of 15 to 30 seconds works best. Longer is not better.
- Keep the style short: 3 to 6 words about instruments, mood and voice.
- Use only audio and lyrics you own or have permission to use.
- Songs stay on the computer until you delete them. They are not copied to the AI-ARK drive.

## Part 4. If it doesn't work

19. Run the doctor: type `aictl` and press **2** (Find and fix problems). Typed in one line: `aictl doctor`.
    - **What you should see:** "Everything is working", or the problems with their codes and fixes.
    - **If it doesn't work:** look up the code in [07-troubleshooting.md](07-troubleshooting.md#music-or-switching-doesnt-work). For music the codes are `LAI-SVC-150` to `LAI-SVC-159`; for switching, `LAI-LLM-040` to `LAI-LLM-043` and `LAI-LLM-050` to `LAI-LLM-053`.
20. If the music program keeps failing, read its log: `aictl logs --unit music`.
    - **What you should see:** the last lines the music program wrote, with times. They never contain your lyrics.
    - **If it doesn't work:** send the last 20 lines and the code to the lab owner.

---

# 12. Hacer una canción, y cambiar lo que hace una computadora

Use esta página cuando quiera que un nodo **haga música** con su letra, o para cambiar una computadora entre tareas como **Programación** y **Música**.

> **Antes de empezar**
> - Necesita: una computadora con tarjeta gráfica NVIDIA que tenga la tarea **Música** (pregunte al dueño del laboratorio, o busque un botón **Música**; la Parte 1 muestra dónde).
> - Por el sitio web: internet, el nodo en modo **online** y una cuenta en `https://ai.marin.cr` que pueda usar música.
> - Borrar una canción pide el **PIN de administrador** en el nodo o en la app de escritorio.
> - **Tiempo:** cambiar a Música toma de 1 a 2 minutos; una canción de 1 minuto toma unos minutos. Son tiempos aproximados; a confirmar en #505.

> 🛠 Verify when implemented (Epic E11: #509, #510, #512, #514, #515).

**Palabras de esta página**
- **Tarea (workload):** lo que la computadora está preparada para hacer ahora, por ejemplo **Programación**, **Conversar y fotos** o **Música**. La tarjeta gráfica tiene espacio para un solo trabajo grande a la vez: al elegir **Música** se detiene la IA de programación, y al elegir **Programación** se detiene la música.
- **Letra:** las palabras de la canción. Las partes se marcan con etiquetas entre corchetes, como `[verse]` (estrofa) y `[chorus]` (coro).
- **Estilo:** unas palabras que describen el sonido, por ejemplo `folk, guitarra acústica, cálido`.
- **Muestra:** un pedazo corto de audio que usted agrega para que la canción lo siga. **Suena como esto** toma su estilo. **La misma canción, mi letra** mantiene su melodía y canta su letra encima.
- **Versión:** la computadora puede hacer hasta 4 canciones distintas con la misma letra a la vez.

**Derechos.** Use solo audio y letras que usted escribió, que son suyos, o que tiene permiso de usar.

**Modo supervivencia.** La música se detiene sola cuando la computadora entra en modo **Supervivencia**, para que la IA de emergencia tenga la tarjeta gráfica. Las canciones ya hechas se guardan. La música no se puede iniciar en modo supervivencia.

---

## Parte 1. Cambiar una computadora a Música, y de vuelta a Programación

Elija **una** de las cuatro formas. Todas hacen lo mismo.

### Desde el sitio web (ai.marin.cr)

1. Abra `https://ai.marin.cr` → **Lab** → presione **Open** en el nodo.
   - **Lo que debería ver:** arriba en la página del nodo, una línea "Esta computadora está en: **Programación**" y un botón por tarea: **Programación**, **Conversar y fotos**, **Música**, etc. La actual se ve presionada.
   - **Si no funciona:** los botones no aparecen si el nodo está en modo **Offline** o **Supervivencia**, o no se ha visto hace rato: use otra forma, en la computadora misma. Un botón gris dice por qué (por ejemplo "no cabe"): vea [07-troubleshooting.md](07-troubleshooting.md#music-or-switching-doesnt-work).
2. Presione **Música**.
   - **Lo que debería ver:** "Ejecutando: Cambiar a Música" y, después de 1 a 2 minutos (aproximado; a confirmar en #505), el botón **Música** se ve presionado.
   - **Si no funciona:** la página muestra un código y su solución. `LAI-LLM-052`: el cambio falló y se volvió a iniciar la IA anterior; intente otra vez. `LAI-LLM-051`: Música no cabe en la tarjeta gráfica de esta computadora. `LAI-LLM-043`: el programa de música no está instalado; presione **Instalar lo que falta** en la página del nodo. Más en [07-troubleshooting.md](07-troubleshooting.md#music-or-switching-doesnt-work).
3. Cuando termine con la música, presione **Programación**.
   - **Lo que debería ver:** el botón **Programación** presionado otra vez en más o menos un minuto.
   - **Si no funciona:** los mismos códigos del paso 2.

### Desde la app de escritorio

4. Abra la app **Local AI**. En **Esta computadora**, busque el cuadro "Lo que hace esta computadora".
   - **Lo que debería ver:** los mismos botones del sitio web, con el actual marcado.
   - **Si no funciona:** si la app no abre, use un navegador en `http://127.0.0.1:7070` (es la misma pantalla).
5. Presione **Música**. Cuando termine, presione **Programación**.
   - **Lo que debería ver:** una barra de progreso ("Deteniendo Programación", "Iniciando Música") y luego la nueva opción marcada.
   - **Si no funciona:** la app muestra el código y la solución, como en el paso 2. `LAI-AGT-022`: se está cargando otra cosa; espere un minuto y presione otra vez.

### Desde la pantalla de la computadora (cualquier navegador)

6. En el nodo, abra un navegador y vaya a `http://127.0.0.1:7070/music/`.
   - **Lo que debería ver:** la página **Música**. Su primera línea dice "La música está lista" o "Esta computadora está en Programación" con un botón **Cambiar a Música**.
   - **Si no funciona:** si la página pide iniciar sesión, escriba el código que muestra `aictl ui-code` (vea [02-chat-and-api-keys.md](02-chat-and-api-keys.md)). Si la página no carga, vea [07-troubleshooting.md](07-troubleshooting.md#music-or-switching-doesnt-work).
7. Presione **Cambiar a Música**.
   - **Lo que debería ver:** "La música está iniciando (cerca de un minuto)" y luego "La música está lista".
   - **Si no funciona:** `LAI-SVC-150`: falta el programa de música; presione **Instalar lo que falta**. `LAI-SVC-159`: el controlador gráfico es muy viejo para la música; use el doctor (Parte 4).

### Desde el menú de la terminal (solo hay que escribir `aictl`)

8. Abra una terminal (**Ctrl + Alt + T**), escriba `aictl` y presione **Enter**.
   - **Lo que debería ver:** el menú, con el estado de la computadora arriba.
   - **Si no funciona:** `LAI-CLI-001` significa que el agente no está corriendo; vea [07-troubleshooting.md](07-troubleshooting.md#music-or-switching-doesnt-work).
9. Presione **4** (modelos de IA) y elija **Cambiar lo que hace esta computadora**.
   - **Lo que debería ver:** la lista de tareas, por ejemplo `Programación (ahora)`, `Conversar y fotos`, `Música`. La pantalla también muestra el comando escrito, `aictl workloads`.
   - **Si no funciona:** una tarea marcada `no cabe` o `falta: …` no se puede elegir; vea [07-troubleshooting.md](07-troubleshooting.md#music-or-switching-doesnt-work).
10. Elija **Música** con las flechas y presione **Enter**.
    - **Lo que debería ver:** `… Deteniendo Qwen 3.5 9B para hacer espacio`, `… Iniciando ACE-Step 1.5 (música)` con una barra, y luego `✓ Esta computadora ahora hace música.` El comando escrito era `aictl workloads activate music`.
    - **Si no funciona:** la línea que empieza con `✗` da el código y la solución (`LAI-LLM-052`, `LAI-LLM-051`, `LAI-LLM-043`). Para volver a programación, haga lo mismo y elija **Programación**.

## Parte 2. Hacer una canción

La canción se hace en el nodo. Puede pedirla en el nodo mismo (la página **Música**, el botón **Música** de la app de escritorio) o desde el sitio web (`https://ai.marin.cr/music`). Los pasos son los mismos; se indican las diferencias.

11. Abra la página **Música**: en la app de escritorio presione **Música** en la barra de arriba; en el navegador del nodo vaya a `http://127.0.0.1:7070/music/`; en el sitio web abra `https://ai.marin.cr/music` y elija el nodo arriba a la derecha.
    - **Lo que debería ver:** la primera línea dice "La música está lista" (en el sitio web: el nodo muestra `Música ✔`) y un formulario con **Letra**, **Estilo**, **Duración**, **Idioma**, **Cuántas versiones**.
    - **Si no funciona:** `LAI-SVC-151` significa que la música no está corriendo en esa computadora: presione **Cambiar a Música** (Parte 1).
12. Escriba la letra en **Letra**. Ponga cada parte bajo una etiqueta. Un ejemplo inventado:

    ```
    [verse]
    Luz de mañana en el corredor
    Café caliente y un poco de sol
    [chorus]
    Vamos caminando sin prisa al mar
    Tú y yo juntos, a descansar
    ```

    - **Lo que debería ver:** el texto en el cuadro. Corto es mejor: unas estrofas y un coro.
    - **Si no funciona:** `LAI-SVC-154` con "letra muy larga": el límite es de unas 8000 letras; quite algunas estrofas.
13. Escriba un **Estilo**, por ejemplo `bolero, guitarra, voz suave`. Elija la **Duración** (por ejemplo 1:00), el **Idioma** de la letra y **Cuántas versiones**: elija **3** o **4** y quédese con la que más le guste.
    - **Lo que debería ver:** sus opciones en el formulario.
    - **Si no funciona:** la duración va de 10 segundos a 4 minutos; el control no va más allá.
14. Opcional: presione **Agregar una muestra** y elija un archivo de audio (WAV, MP3, FLAC u Ogg). Use **15 a 30 segundos** de audio sobre el que tenga derechos. Luego elija:
    - **Suena como esto**: una canción nueva con el estilo de la muestra.
    - **La misma canción, mi letra**: la melodía de la muestra, cantada con su letra.

    En **Más opciones**, **Qué tan parecido** define cuánto sigue la canción a la muestra.
    - **Lo que debería ver:** el nombre del archivo junto a **Agregar una muestra**, y una de las dos opciones marcada.
    - **Si no funciona:** `LAI-SVC-154`: el archivo no es audio, pesa más de 20 MB o dura más de 60 segundos. Córtelo a 15 o 30 segundos e intente otra vez.
15. Presione **Hacer la canción**.
    - **Lo que debería ver:** una barra de progreso en **Solicitudes** (en el sitio web: "en curso") y luego las canciones nuevas arriba en **Mis canciones**. Una canción de 1 minuto toma unos minutos (aproximado; a confirmar en #505).
    - **Si no funciona:** `LAI-SVC-153`: ya hay cuatro canciones esperando; espere a que termine una. `LAI-SVC-155`: el disco está casi lleno; borre canciones viejas (paso 18). `LAI-SVC-152`: el programa de música se detuvo a la mitad; presione **Cambiar a Música** otra vez y reintente. En el sitio web también `LAI-SVC-156` (la muestra no llegó al nodo: envíela otra vez) y `LAI-SVC-157` (la canción no se pudo devolver; queda en el nodo). Para detener una canción presione **Cancelar**; muestra `LAI-SVC-158`, que no es un error.
16. Presione **▶** en una canción para escucharla.
    - **Lo que debería ver:** la canción suena. El título es la primera línea de su letra.
    - **Si no funciona:** suba el volumen de la computadora; pruebe otra versión.
17. Presione **Descargar** para guardar la canción.
    - **Lo que debería ver:** un archivo `.wav` en su carpeta **Descargas** (en la app de escritorio: en su carpeta **Música** o **Descargas**).
    - **Si no funciona:** revise que el disco de su computadora no esté lleno.
18. Para borrar una canción que no quiere, presione **Borrar** y confirme. En el nodo y en la app de escritorio, escriba el **PIN de administrador** cuando lo pida.
    - **Lo que debería ver:** la canción desaparece de **Mis canciones**. Borrar en el sitio web borra solo la copia del sitio; la del nodo se queda hasta que la borre allí.
    - **Si no funciona:** un PIN equivocado muestra `LAI-SEC-002`; intente otra vez. Una canción que se está haciendo no se puede borrar; espere o presione **Cancelar** primero.

## Parte 3. Consejos

- Haga 3 o 4 versiones y quédese con la mejor: cada versión es distinta.
- Una muestra de 15 a 30 segundos funciona mejor. Más larga no es mejor.
- Escriba un estilo corto: de 3 a 6 palabras sobre instrumentos, ánimo y voz.
- Use solo audio y letras que son suyos o que tiene permiso de usar.
- Las canciones se quedan en la computadora hasta que las borre. No se copian al disco AI-ARK.

## Parte 4. Si no funciona

19. Use el doctor: escriba `aictl` y presione **2** (Encontrar y arreglar problemas). En una línea: `aictl doctor`.
    - **Lo que debería ver:** "Todo funciona", o los problemas con sus códigos y soluciones.
    - **Si no funciona:** busque el código en [07-troubleshooting.md](07-troubleshooting.md#music-or-switching-doesnt-work). Para música los códigos son `LAI-SVC-150` a `LAI-SVC-159`; para cambiar de tarea, `LAI-LLM-040` a `LAI-LLM-043` y `LAI-LLM-050` a `LAI-LLM-053`.
20. Si el programa de música sigue fallando, lea su registro: `aictl logs --unit music`.
    - **Lo que debería ver:** las últimas líneas que escribió el programa de música, con la hora. Nunca contienen su letra.
    - **Si no funciona:** envíe las últimas 20 líneas y el código al dueño del laboratorio.
