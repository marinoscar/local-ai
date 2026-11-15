# 12. Make a song, and switch what a computer does

Use this when you want a node to **make music** from your lyrics, or to switch a computer between jobs such as **Coding** and **Music**.

> **Before you start**
> - You need: a computer with an NVIDIA graphics card that has the **Music** workload (ask the lab owner, or look for a **Music** button; Part 1 shows where).
> - For the website way: internet, the node in **online** mode, and an account on `https://ai.marin.cr` that may use music.
> - Deleting a song needs the **admin PIN** on the node or in the desktop app.
> - **Time:** switching to Music takes about 1 minute (the music engine itself starts in about 20 seconds); a 1-minute song takes about 20 seconds on a 12 GB graphics card.

> 🛠 Verify when implemented (Epic E11: #509 workloads, #510 music API, #512 local page, #514 website page, #515 desktop app, #568 website prompts and titles, #570 website AI for music and optional lyrics).

**Words used on this page**
- **Workload:** what the computer is set up to do right now, for example **Coding**, **Chat and photos** or **Music**. The graphics card has room for only one big job at a time, so choosing **Music** stops the coding AI, and choosing **Coding** stops the music maker.
- **Lyrics:** the words of the song. Parts of the song are marked with tags in square brackets, such as `[verse]` and `[chorus]`.
- **Style:** a few words that describe the sound, for example `folk, acoustic guitar, warm`. On the website this box is called **Describe your song**, and you may write a whole sentence.
- **Prompt:** on the website, everything you typed for a song (title, description, lyrics, settings and sample). The website keeps your prompts under **My prompts**, so you can open one again, change it, make it again or delete it (Part 5).
- **Music AI:** on the website, an AI service that writes lyrics and suggests titles. The lab owner turns it on once (Part 6). Without it, everything else still works: empty lyrics make a song with no vocals.
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
   - **What you should see:** "Running command: Switch to Music", then after about 1 minute the **Music** button looks pressed.
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
   - **If it doesn't work:** `LAI-SVC-150`: the music program is missing; press **Install what's missing**. If that says the kit does not have it (`LAI-KIT-040`), connect the computer to the internet, run `aictl ark sync --item kit-engine-acestep-cuda --wait` (it downloads about 4.5 GB, and continues where it stopped if the connection drops), then press **Install what's missing** again. `LAI-SVC-159`: the graphics driver is too old for music; run the doctor (Part 4).

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
    - **What you should see:** the first line says "Music is ready" (on the website: the node shows `Music ✔`), and a form with **Lyrics**, **Style**, **Length**, **Language**, **How many versions**. On the website the form starts with **Title** and **Describe your song**, and under **Lyrics** you choose **Use my lyrics**, **Write lyrics for me** or **No vocals (instrumental)** (Part 5).
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
13. Type a **Style**, for example `folk, acoustic guitar, warm, female voice`. Leave **Length** on **Auto**: the music model picks the length from your lyrics and the style (more lyrics make a longer song). To choose it yourself, press **Set the length** and type the seconds (for example `60` for 1:00). Then choose the **Language** of the lyrics and **How many versions**: choose **3** or **4**, then keep the one you like best.
    - **What you should see:** your choices in the form, with "Length: Auto" unless you set one. When the song is ready, the list shows the length the model chose.
    - **If it doesn't work:** a set length goes from 10 to 240 seconds (4 minutes); `LAI-SVC-154` with "duration_s" means the number is outside that range. On the website, the note "This computer needs an update to pick the length by itself; using 1 minute" means that computer runs an older Local AI: the song is made at 1 minute. Update that computer ([runbook 10, Part 4](10-lab-operations.md#part-4-updating-agents)) to get Auto there.
14. Optional: press **Add a sample** and choose an audio file (WAV, MP3, FLAC or Ogg). Use **15 to 30 seconds** of audio you have the rights to. Then choose:
    - **Sounds like this**: a new song in the style of the sample.
    - **Same song, my lyrics**: the sample's melody, sung with your lyrics.

    Under **More options**, **How closely** sets how much the song follows the sample.
    - **What you should see:** the file name next to **Add a sample**, and one of the two choices marked.
    - **If it doesn't work:** `LAI-SVC-154`: the file is not audio, is bigger than 20 MB, or longer than 60 seconds. Cut it to 15 to 30 seconds and try again.
15. Press **Make the song**.
    - **What you should see:** a progress bar under **Requests** (on the website: "running"), then the new songs at the top of **My songs**. A 1-minute song takes about 20 seconds on a 12 GB graphics card, a little more on the website.
    - **If it doesn't work:** `LAI-SVC-153`: four songs are already waiting; wait for one to finish. `LAI-SVC-155`: the disk is almost full; delete old songs (step 18). `LAI-SVC-152`: the music program stopped in the middle; press **Switch to Music** again and retry. On the website also `LAI-SVC-156` (the sample did not reach the node: send it again) and `LAI-SVC-157` (the song could not be sent back; it is kept on the node). To stop a song, press **Cancel**; it shows `LAI-SVC-158`, which is not an error.
16. Press **▶** on a song to listen.
    - **What you should see:** the song plays. The title is the first line of your lyrics (on the website: the **Title** you typed, or one made from your words).
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

## Part 5. On the website: your prompts and song titles

The website (`https://ai.marin.cr/music`) keeps every prompt you make a song from under **My prompts**. A song keeps the title and settings it was made with, even if you change or delete its prompt later.

21. Write the song's words for the website. In **Describe your song**, write the song in plain words, for example `a slow ballad about the sea, soft piano, warm voice, 80 bpm` (at most 500 characters). Lyrics are optional: you do not have to choose anything. Under **Lyrics**, **Automatic (recommended)** is already chosen:
    - **Automatic (recommended)**: type your lyrics in **Your lyrics**, or leave the box empty. The line under the box says what happens when you press **Make the song**: "Your lyrics are used.", "Leave it empty and the AI writes the lyrics when you press Make the song." or "Leave it empty for a song with no vocals."
    - **Use my lyrics**: type or paste your lyrics in **Your lyrics**.
    - **Write lyrics now**: press **Write the lyrics**. The AI writes lyrics from your description and puts them in **Your lyrics**. Read them and change what you want.
    - **No vocals (instrumental)**: the song is made without singing.
    - **What you should see:** the counter under **Describe your song** (for example `60 / 500 characters`), and the line under the lyrics box, or the line "The song is made without singing." When the AI wrote your lyrics, they are in the form after the song is made: change them and press **Make the song** again if you want.
    - **If it doesn't work:** if **Write lyrics now** is grey, the line under the choices says why and what to do: the AI is turned off (Settings → AI), **Use AI for music** is turned off, or no AI model is set up for you. The lab owner fixes it with Part 6. You can still make the song: leave the lyrics empty for a song with no vocals, or write them yourself. "The AI could not write lyrics this time" means the AI did not answer: press **Make the song** again, write your own lyrics, or choose **No vocals**. "The description is too long for the computer" means it has many accented letters or symbols: shorten it a little.
22. Give the song a title. Type it in **Title** (at most 80 characters), or press **Suggest a title**. Or leave it empty.
    - **What you should see:** a title in the box, with "Suggested by the AI" or "Made from your words" under it. If you leave it empty, the song gets the first line of your lyrics, or the first words of your description, or "Untitled song".
    - **If it doesn't work:** **Suggest a title** always gives a title; when the AI is off it makes one from your words. Change it if you don't like it.
23. Press **Make the song** (Part 2, step 15).
    - **What you should see:** the song under **Requests**, and your prompt at the top of **My prompts**, open in the form.
    - **If it doesn't work:** see step 15.
24. To use an old prompt again, find it under **My prompts** (each one shows its title, its description, when you last used it and how many songs it made) and press **Open**.
    - **What you should see:** the form filled with that prompt, its title at the top ("Prompt: …"), and the buttons **Save**, **Save as new** and **Delete**. If it has a saved sample, the line "This prompt has a saved sample" is shown.
    - **If it doesn't work:** press **Try again** next to "Could not load your prompts", or reload the page.
25. Change what you want, then choose:
    - **Save**: keeps the changes in this prompt. Songs you already made do not change.
    - **Save as new**: keeps the changes as a new prompt, and the old one stays as it was (with the same sample).
    - **Make the song**: makes a song now and saves what you typed into this prompt. The saved sample is used again; press **Remove sample** first if you do not want it.
    - **What you should see:** "Saved.", or the new song under **Requests**. The prompt moves to the top of **My prompts**.
    - **If it doesn't work:** an error with a code: see step 15 and Part 4.
26. To start from nothing, press **New prompt**.
    - **What you should see:** an empty form titled "New song".
    - **If it doesn't work:** reload the page.
27. To delete a prompt, open it and press **Delete**, then **Delete** again in the box that asks.
    - **What you should see:** the box says the songs stay in the library. The prompt leaves **My prompts**; its songs stay under **Songs**, marked "prompt deleted".
    - **If it doesn't work:** an error says why; try again. Deleting a prompt never deletes a song. To delete a song, use step 18.

## Part 6. For the lab owner: let the website use AI for music (once)

The website can write lyrics and suggest titles with an AI service you choose (for example OpenAI, or any OpenAI-compatible server). It is off until you do these steps. You need an administrator account on the website and an API key from the AI service.

28. Open `https://ai.marin.cr/admin/settings` and press the **AI** card (in the **AI** group). Switch AI on, open the provider you want, switch it on, paste its API key and save.
    - **What you should see:** AI is on, and the provider shows that a key is stored. The key is never shown again.
    - **If it doesn't work:** the page says what is wrong (for example a key that the provider refused). Check the key with the AI service, then save again.
29. Go back to `https://ai.marin.cr/admin/settings` and press **AI Models**. Turn on at least one model that writes text.
    - **What you should see:** the model is listed as enabled.
    - **If it doesn't work:** if the list is empty, the provider in step 28 is off or its key was refused: repeat step 28.
30. Go back to `https://ai.marin.cr/admin/settings` and press **Use AI for music** (in the **AI** group). Turn on the **Use AI for music** switch.
    - **What you should see:** "Music AI is on."
    - **If it doesn't work:** the switch is grey when your account cannot change AI settings: sign in as the administrator. The card is hidden while AI is off: do step 28 first.
31. Check it: open `https://ai.marin.cr/music`, write a description and leave **Your lyrics** empty.
    - **What you should see:** the line "Leave it empty and the AI writes the lyrics when you press Make the song.", and **Write lyrics now** can be chosen.
    - **If it doesn't work:** the line under the lyrics choices names what is still off; fix that step. If it says no AI model is set up for you, step 29 has no enabled model, or the provider's key does not pay for your account. See [07-troubleshooting.md](07-troubleshooting.md).

---

# 12. Hacer una canción, y cambiar lo que hace una computadora

Use esta página cuando quiera que un nodo **haga música** con su letra, o para cambiar una computadora entre tareas como **Programación** y **Música**.

> **Antes de empezar**
> - Necesita: una computadora con tarjeta gráfica NVIDIA que tenga la tarea **Música** (pregunte al dueño del laboratorio, o busque un botón **Música**; la Parte 1 muestra dónde).
> - Por el sitio web: internet, el nodo en modo **online** y una cuenta en `https://ai.marin.cr` que pueda usar música.
> - Borrar una canción pide el **PIN de administrador** en el nodo o en la app de escritorio.
> - **Tiempo:** cambiar a Música toma alrededor de 1 minuto (el motor de música arranca en unos 20 segundos); una canción de 1 minuto toma unos 20 segundos con una tarjeta gráfica de 12 GB.

> 🛠 Verify when implemented (Epic E11: #509, #510, #512, #514, #515, #568, #570).

**Palabras de esta página**
- **Tarea (workload):** lo que la computadora está preparada para hacer ahora, por ejemplo **Programación**, **Conversar y fotos** o **Música**. La tarjeta gráfica tiene espacio para un solo trabajo grande a la vez: al elegir **Música** se detiene la IA de programación, y al elegir **Programación** se detiene la música.
- **Letra:** las palabras de la canción. Las partes se marcan con etiquetas entre corchetes, como `[verse]` (estrofa) y `[chorus]` (coro).
- **Estilo:** unas palabras que describen el sonido, por ejemplo `folk, guitarra acústica, cálido`. En el sitio web este cuadro se llama **Describa su canción**, y puede escribir una frase completa.
- **Indicación:** en el sitio web, todo lo que escribió para una canción (título, descripción, letra, opciones y muestra). El sitio guarda sus indicaciones en **Mis indicaciones**, para abrirlas otra vez, cambiarlas, volver a usarlas o eliminarlas (Parte 5).
- **IA para música:** en el sitio web, un servicio de IA que escribe letras y sugiere títulos. El dueño del laboratorio la enciende una vez (Parte 6). Sin ella todo lo demás funciona igual: sin letra, la canción no tiene voz.
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
   - **Lo que debería ver:** "Ejecutando: Cambiar a Música" y, después de alrededor de 1 minuto, el botón **Música** se ve presionado.
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
   - **Si no funciona:** `LAI-SVC-150`: falta el programa de música; presione **Instalar lo que falta**. Si dice que el kit no lo tiene (`LAI-KIT-040`), conecte la computadora a internet, ejecute `aictl ark sync --item kit-engine-acestep-cuda --wait` (descarga unos 4,5 GB y continúa donde quedó si se corta la conexión) y presione **Instalar lo que falta** otra vez. `LAI-SVC-159`: el controlador gráfico es muy viejo para la música; use el doctor (Parte 4).

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
    - **Lo que debería ver:** la primera línea dice "La música está lista" (en el sitio web: el nodo muestra `Música ✔`) y un formulario con **Letra**, **Estilo**, **Duración**, **Idioma**, **Cuántas versiones**. En el sitio web el formulario empieza con **Título** y **Describa su canción**, y en **Letra** elige **Usar mi letra**, **Escribir la letra por mí** o **Sin voz (instrumental)** (Parte 5).
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
13. Escriba un **Estilo**, por ejemplo `bolero, guitarra, voz suave`. Deje la **Duración** en **Automática**: el modelo de música elige la duración según su letra y el estilo (más letra hace una canción más larga). Para elegirla usted, presione **Elegir la duración** y escriba los segundos (por ejemplo `60` para 1:00). Luego elija el **Idioma** de la letra y **Cuántas versiones**: elija **3** o **4** y quédese con la que más le guste.
    - **Lo que debería ver:** sus opciones en el formulario, con "Duración: Automática" si no eligió una. Cuando la canción está lista, la lista muestra la duración que eligió el modelo.
    - **Si no funciona:** una duración elegida va de 10 a 240 segundos (4 minutos); `LAI-SVC-154` con "duration_s" significa que el número está fuera de ese rango. En el sitio web, la nota "Esta computadora necesita una actualización para elegir la duración por sí sola; se usa 1 minuto" significa que esa computadora tiene un Local AI más antiguo: la canción se hace de 1 minuto. Actualice esa computadora ([runbook 10, Part 4](10-lab-operations.md#part-4-updating-agents)) para tener la duración automática allí.
14. Opcional: presione **Agregar una muestra** y elija un archivo de audio (WAV, MP3, FLAC u Ogg). Use **15 a 30 segundos** de audio sobre el que tenga derechos. Luego elija:
    - **Suena como esto**: una canción nueva con el estilo de la muestra.
    - **La misma canción, mi letra**: la melodía de la muestra, cantada con su letra.

    En **Más opciones**, **Qué tan parecido** define cuánto sigue la canción a la muestra.
    - **Lo que debería ver:** el nombre del archivo junto a **Agregar una muestra**, y una de las dos opciones marcada.
    - **Si no funciona:** `LAI-SVC-154`: el archivo no es audio, pesa más de 20 MB o dura más de 60 segundos. Córtelo a 15 o 30 segundos e intente otra vez.
15. Presione **Hacer la canción**.
    - **Lo que debería ver:** una barra de progreso en **Solicitudes** (en el sitio web: "en curso") y luego las canciones nuevas arriba en **Mis canciones**. Una canción de 1 minuto toma unos 20 segundos con una tarjeta gráfica de 12 GB, un poco más en el sitio web.
    - **Si no funciona:** `LAI-SVC-153`: ya hay cuatro canciones esperando; espere a que termine una. `LAI-SVC-155`: el disco está casi lleno; borre canciones viejas (paso 18). `LAI-SVC-152`: el programa de música se detuvo a la mitad; presione **Cambiar a Música** otra vez y reintente. En el sitio web también `LAI-SVC-156` (la muestra no llegó al nodo: envíela otra vez) y `LAI-SVC-157` (la canción no se pudo devolver; queda en el nodo). Para detener una canción presione **Cancelar**; muestra `LAI-SVC-158`, que no es un error.
16. Presione **▶** en una canción para escucharla.
    - **Lo que debería ver:** la canción suena. El título es la primera línea de su letra (en el sitio web: el **Título** que escribió, o uno hecho con sus palabras).
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

## Parte 5. En el sitio web: sus indicaciones y los títulos

El sitio web (`https://ai.marin.cr/music`) guarda en **Mis indicaciones** cada indicación con la que crea una canción. Una canción conserva el título y las opciones con que se hizo, aunque después cambie o elimine su indicación.

21. Escriba las palabras de la canción para el sitio web. En **Describa su canción**, describa la canción con palabras sencillas, por ejemplo `una balada lenta sobre el mar, piano suave, voz cálida, 80 bpm` (500 caracteres como máximo). La letra es opcional: no tiene que elegir nada. En **Letra**, **Automático (recomendado)** ya está elegido:
    - **Automático (recomendado)**: escriba su letra en **Su letra**, o deje el cuadro vacío. La línea bajo el cuadro dice qué pasa al presionar **Crear la canción**: "Se usa su letra.", "Déjela vacía y la IA escribe la letra cuando pulse Crear la canción." o "Déjela vacía para una canción sin voz."
    - **Usar mi letra**: escriba o pegue su letra en **Su letra**.
    - **Escribir la letra ahora**: presione **Escribir la letra**. La IA escribe una letra con su descripción y la pone en **Su letra**. Léala y cambie lo que quiera.
    - **Sin voz (instrumental)**: la canción se hace sin canto.
    - **Lo que debería ver:** el contador bajo **Describa su canción** (por ejemplo `60 / 500 caracteres`), y la línea bajo el cuadro de la letra, o la línea "La canción se hace sin canto." Si la IA escribió su letra, la letra queda en el formulario después de crear la canción: cámbiela y presione **Crear la canción** otra vez si quiere.
    - **Si no funciona:** si **Escribir la letra ahora** está gris, la línea bajo las opciones dice por qué y qué hacer: la IA está apagada (Ajustes (Settings) → AI), **Use AI for music** está apagado, o no hay un modelo de IA para usted. El dueño del laboratorio lo arregla con la Parte 6. Igual puede crear la canción: deje la letra vacía para una canción sin voz, o escríbala usted. "La IA no pudo escribir la letra esta vez" quiere decir que la IA no respondió: presione **Crear la canción** otra vez, escriba su propia letra, o elija **Sin voz**. "La descripción es demasiado larga para la computadora" quiere decir que tiene muchas letras con tilde o símbolos: acórtela un poco.
22. Póngale un título a la canción. Escríbalo en **Título** (80 caracteres como máximo), o presione **Sugerir un título**. O déjelo vacío.
    - **Lo que debería ver:** un título en el cuadro, con "Sugerido por la IA" o "Hecho con sus palabras" debajo. Si lo deja vacío, la canción recibe la primera línea de su letra, o las primeras palabras de su descripción, o "Canción sin título".
    - **Si no funciona:** **Sugerir un título** siempre da un título; con la IA apagada lo hace con sus palabras. Cámbielo si no le gusta.
23. Presione **Crear la canción** (Parte 2, paso 15).
    - **Lo que debería ver:** la canción en **Solicitudes**, y su indicación arriba en **Mis indicaciones**, abierta en el formulario.
    - **Si no funciona:** vea el paso 15.
24. Para usar otra vez una indicación vieja, búsquela en **Mis indicaciones** (cada una muestra su título, su descripción, cuándo la usó por última vez y cuántas canciones hizo) y presione **Abrir**.
    - **Lo que debería ver:** el formulario lleno con esa indicación, su título arriba ("Indicación: …") y los botones **Guardar**, **Guardar como nueva** y **Eliminar**. Si tiene una muestra guardada, se ve la línea "Esta indicación tiene una muestra guardada".
    - **Si no funciona:** presione **Intentar otra vez** junto a "No se pudieron cargar sus indicaciones", o recargue la página.
25. Cambie lo que quiera y luego elija:
    - **Guardar**: guarda los cambios en esta indicación. Las canciones que ya hizo no cambian.
    - **Guardar como nueva**: guarda los cambios como una indicación nueva, y la vieja queda como estaba (con la misma muestra).
    - **Crear la canción**: crea una canción ahora y guarda lo que escribió en esta indicación. La muestra guardada se usa otra vez; presione antes **Quitar la muestra** si no la quiere.
    - **Lo que debería ver:** "Guardado.", o la canción nueva en **Solicitudes**. La indicación sube al primer lugar de **Mis indicaciones**.
    - **Si no funciona:** un error con un código: vea el paso 15 y la Parte 4.
26. Para empezar de cero, presione **Nueva indicación**.
    - **Lo que debería ver:** un formulario vacío con el título "Canción nueva".
    - **Si no funciona:** recargue la página.
27. Para eliminar una indicación, ábrala, presione **Eliminar** y luego **Eliminar** otra vez en el cuadro que pregunta.
    - **Lo que debería ver:** el cuadro dice que las canciones se quedan en la biblioteca. La indicación sale de **Mis indicaciones**; sus canciones se quedan en **Canciones**, marcadas "indicación eliminada".
    - **Si no funciona:** un error dice por qué; intente otra vez. Eliminar una indicación nunca borra una canción. Para borrar una canción, use el paso 18.

## Parte 6. Para el dueño del laboratorio: dejar que el sitio web use IA para música (una vez)

El sitio web puede escribir letras y sugerir títulos con un servicio de IA que usted elige (por ejemplo OpenAI, o cualquier servidor compatible con OpenAI). Está apagado hasta que haga estos pasos. Necesita una cuenta de administrador en el sitio web y una clave de API del servicio de IA. Las páginas de administración están en inglés: los nombres de los botones se dan como aparecen.

28. Abra `https://ai.marin.cr/admin/settings` y presione la tarjeta **AI** (en el grupo **AI**). Encienda la IA, abra el proveedor que quiere, enciéndalo, pegue su clave de API y guarde.
    - **Lo que debería ver:** la IA está encendida y el proveedor muestra que tiene una clave guardada. La clave nunca se vuelve a mostrar.
    - **Si no funciona:** la página dice qué está mal (por ejemplo una clave que el proveedor rechazó). Revise la clave con el servicio de IA y guarde otra vez.
29. Vuelva a `https://ai.marin.cr/admin/settings` y presione **AI Models**. Encienda al menos un modelo que escriba texto.
    - **Lo que debería ver:** el modelo aparece como habilitado.
    - **Si no funciona:** si la lista está vacía, el proveedor del paso 28 está apagado o su clave fue rechazada: repita el paso 28.
30. Vuelva a `https://ai.marin.cr/admin/settings` y presione **Use AI for music** (en el grupo **AI**). Encienda el interruptor **Usar IA para música**.
    - **Lo que debería ver:** "La IA para música está encendida."
    - **Si no funciona:** el interruptor está gris si su cuenta no puede cambiar los ajustes de IA: entre como administrador. La tarjeta no se ve mientras la IA está apagada: haga primero el paso 28.
31. Compruébelo: abra `https://ai.marin.cr/music`, escriba una descripción y deje **Su letra** vacía.
    - **Lo que debería ver:** la línea "Déjela vacía y la IA escribe la letra cuando pulse Crear la canción.", y **Escribir la letra ahora** se puede elegir.
    - **Si no funciona:** la línea bajo las opciones de la letra dice qué sigue apagado; arregle ese paso. Si dice que no hay un modelo de IA para usted, el paso 29 no tiene un modelo habilitado, o la clave del proveedor no paga por su cuenta. Vea [07-troubleshooting.md](07-troubleshooting.md).
