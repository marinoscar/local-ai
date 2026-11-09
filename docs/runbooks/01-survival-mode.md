# 01. Survival mode: the emergency guide

**English first. Versión completa en español más abajo (página "01. Modo supervivencia").**

Print this page and keep it next to the computer, with the AI-ARK drive. The one-page short version is the [Survival card](00-survival-card.md).

> **Before you start**
> - You need: the AI laptop (or AI computer), its charger, and the name and password of its user.
> - Helpful: the AI-ARK drive (a small portable disk labelled **AI-ARK**), a mouse, headphones with a microphone.
> - You do **not** need the internet. You do **not** need the admin PIN to start Survival mode.
> - **Time:** about 5 minutes to get going.

> 🛠 Verify when implemented (Epics E4 Survival Portal, E8 desktop app). The screens below follow the design; check names and timings on a real node.

---

## Part 1. Turn it on and log in

1. **Plug in the charger** if you can. Then press the power button once.
   - **What you should see:** the maker's logo, then Ubuntu starting. Wait up to 2 minutes.
   - **If it doesn't work:** hold the power button for 10 seconds, let go, wait 5 seconds, press it once. Still dark: charge it for 30 minutes and try again.

2. **If the screen asks for a "passphrase" or "disk password" before anything else:** this computer's disk is encrypted. Type that passphrase and press **Enter**.
   - **What you should see:** Ubuntu continues starting.
   - **If it doesn't work:** check the keyboard language and Caps Lock. Without this passphrase nobody can open the disk; use the AI-ARK drive on another computer instead (Part 9).

3. **Log in.** Click the user name (`<DESKTOP_USER>`), type its password, press **Enter**.
   - **What you should see:** the desktop. After a few seconds the **Local AI** app may open by itself. If the computer was already in Survival mode, the big-button screen (the **Survival Portal**) fills the screen. Then skip to Part 3.
   - **If it doesn't work:** the password is not written on this page on purpose. Ask the owner, or check where the lab keeps it.

---

## Part 2. Start Survival mode

4. **Open the Local AI app.** If it isn't open: press the **Super** key (the Windows-logo key), type `Local AI`, press **Enter**.
   - **What you should see:** a window called **Local AI**.
   - **If it doesn't work:** go to step 6 (the browser way).

5. **Press Survival mode.** Click the small Local AI icon at the top of the screen (the tray menu), choose **Mode**, then **⚠ Survival mode**, and confirm with **Switch to Survival mode**. Or, in the window, press the **Start survival mode** button (it needs no confirmation).
   - **What you should see:** within about 2 minutes the Survival Portal fills the screen. The top line says **Ready**, or **Ready, with problems**, with a short list.
   - **If it doesn't work:** go to step 6.

6. **The browser way (if the app doesn't open).** Open any web browser (Firefox is in the dock on the left). Click the address bar at the top, type exactly:

   ```
   http://127.0.0.1:7070
   ```

   and press **Enter**. If you see a yellow bar **Survival mode is off**, press **Start survival mode**.
   - **What you should see:** the Survival Portal with 8 big buttons.
   - **If it doesn't work:** go to step 7.

7. **The terminal way (if the browser shows nothing).** Press **Ctrl + Alt + T**. A terminal window opens. Type `aictl` and press **Enter**. A menu opens. Press **5** (Mode), then **4** (Survival). Wait until it says `Survival mode is on`, press **Enter** to go back to the menu, then **q** to close it. Then do step 6 again.

   The same thing typed in one line, if you prefer:

   ```
   aictl mode survival
   ```

   - **What you should see:** lines like `Starting the library and speech`, then `✓ Survival mode is on`.
   - **If it doesn't work:** a message with a code like `[LAI-CLI-001]` appears with a fix line under it. Do what the fix line says. Also see Part 8 below and runbook 07, section **"The app won't open"**.

---

## Part 3. The Survival Portal, button by button

You can click a button, or press its number on the keyboard. **Esc** always brings you back to the home screen. The **English | Español** switch at the top changes the language. **A A+ A++** makes the text bigger.

### The Local AI app in Survival mode (full screen)

When the computer is in Survival mode, the **Local AI** app fills the whole screen with the Survival Portal. A thin orange strip at the top says **Survival mode**. It has an **EN | ES** switch for the language and an **Exit full screen** button.

- **To make the window smaller (for example to use another program):** press **Exit full screen** in the strip. The strip stays, and the button now says **Full screen**: press it to fill the screen again. The F11 key does the same. This does **not** turn Survival mode off.
- **If you close the window:** the app keeps running in the background. Click the small Local AI icon at the top of the screen (the tray), or start **Local AI** again from the Super key menu: the Survival Portal fills the screen again. To really quit, use **Quit** in the tray menu. The computer keeps running Survival mode even when the app is closed.
- **The language switch in the strip** changes the strip and the Portal together. By default the language follows the computer's own language.
- **If the strip shows a code such as `LAI-DSK-001`** (this user is not allowed to control Local AI): the Portal still works, everything on it is available. The code tells the owner what to fix later: see the troubleshooting runbook.
- **What you should see:** the Portal with its big buttons under a 1-line strip. **If it doesn't work:** use the browser way (step 6).

### Button 1: Chat (type questions)
8. Press **1**. After a short "Opening the chat" message, the chat page opens.
9. Click the box at the bottom, type your question in plain words, press **Enter**.
   - **What you should see:** the answer appears word by word, usually within 30 seconds.
   - **To come back:** press the browser **Back** button, or type `127.0.0.1:7070/portal` in the address bar.
   - **If it doesn't work:** "No brain is running" → press **Choose a brain** (see button 8). Codes `LAI-SVC-102` or `LAI-SVC-106`: see Part 8.

### Button 2: Ask about a photo
10. Press **2**. Press **Take photo** (uses the laptop camera) or **Choose file** (a picture already on the computer).
11. Type a question, or press a quick question: **What is this?**, **Is it dangerous?**, **How do I fix this?**, **Read the text**. Press **Ask**.
   - **What you should see:** if the current brain can't see photos, a box offers to switch to one that can. Press **Switch** and wait about a minute. Then the answer appears. **Read aloud** speaks it.
   - **Important:** the AI can be wrong. Never eat or touch a plant, mushroom or animal just because the AI says it is safe.
   - **If it doesn't work:** "No brain that understands photos is installed" (`LAI-SVC-040`): this computer can't do photos; use Chat and describe the thing in words. "This photo can't be opened" (`LAI-SVC-108`): try another photo.

### Button 3: Talk (speak and listen)
12. Press **3**. The first time, the browser asks to use the microphone: press **Allow**.
13. Press **Space** (or click the big microphone), speak your question, then stop talking. It stops by itself after a short silence.
   - **What you should see:** "You said: …" with your words, then "Thinking …", then the answer is spoken aloud and shown as text. **Stop speaking** stops it; **Repeat** says it again. **Edit** lets you fix the words before sending.
   - **If it doesn't work:** "The microphone is blocked" (`LAI-SVC-103`): click the lock icon next to the address bar and allow the microphone, or plug in a headset. "I couldn't hear anything": speak closer. No voice, only text (`LAI-SVC-022`): read the answer on screen.

### Button 4: Library (Wikipedia, medicine, how-to)
14. Press **4**. Type what you are looking for (for example `water purification`) and press **Enter**.
15. Click a result to read it. **Back to results** returns to the list. The **Books** list on the right opens one book (for example WikiMed for medicine, iFixit for repairs).
   - **What you should see:** a list of articles; the article opens inside the Portal.
   - **If it doesn't work:** "The library has no books" (`LAI-SVC-001`): plug in the AI-ARK drive, wait 10 seconds, then press the button **Copy from the AI-ARK drive** on that screen. It says "Copying from the drive has started"; the books appear by themselves when it is done. "The library is not running" (`LAI-SVC-002`): press **Restart the library**, or see Part 8.

### First aid (press F)
16. On the home screen, press **F**, or click a button in the **FIRST AID** row (CPR, Choking, Severe bleeding, Burns, Broken bone, Snake bite, **More …**).
   - **What you should see:** the article opens at once.
   - **This is not a doctor.** If any phone works, call the emergency number shown on the screen (911 in Costa Rica). Act first, read later.
   - **If it doesn't work:** "The medical book is not installed": plug in the AI-ARK drive, or use Library search.

### Button 5: Maps
17. Press **5**. Type a town or place in the search box and press **Enter**.
18. **Where am I** shows your position if a GPS is plugged in. Without GPS, right-click (or long-press) the map where you are and choose **I am here**.
19. **Nearest: hospital · clinic · water · town** lists the closest ones to your position.
   - **What you should see:** a map with streets and names. It works with no internet.
   - **If it doesn't work:** "No offline map is installed" (`LAI-SVC-030`): plug in the AI-ARK drive. No names on the map (`LAI-SVC-031`): the map works, only the labels are missing.

### Button 6: Manuals
20. Press **6**. Choose a manual: **01. Survival mode: the emergency guide** (this page), **06. Rebuild a node with no internet**, **07. Troubleshooting**. **Print the Survival card** gives a one-page summary to print (a PDF; on a computer without a PDF maker it is a web page, and you print it from the browser: `LAI-CLI-014`, information only). The card never shows a password, key or address; if `sudo aictl docs render` refuses to make it, it says `LAI-CLI-016`.

### Button 7: Status and battery
21. Press **7**.
   - **What you should see:** the battery level and time left, which brain is running, the mode, the ark colour, the clock, **What works** and **Problems**. Each problem has a code and a one-line fix. Some problems also have a button that does the fix for you (for example **Restart the library** or **Copy from the AI-ARK drive**): press it and read the line under it.
   - **Ark colour:** **Green** = everything is here and up to date. **Yellow** = everything essential is here. **Red** = something essential is missing; read the problems.

### Button 8: Switch brain
22. Press **8**. You see the brains you can use. Each says what it is good for: **Fast**, **Sees photos**, **Very small … Slower answers**.
23. Press **Use this** on the one you want, then confirm.
   - **What you should see:** "Loading … 35 s". Switching takes about a minute. Questions in progress stop.
   - **If it doesn't work:** "couldn't start. The previous brain was restarted." (`LAI-SVC-107`): choose a smaller brain.

---

## Part 4. Saving battery

<!-- runbook-lint: no-expect-section (advice) -->

When there is no charger, the battery is your fuel. The AI uses a lot of power **while it is answering**, and very little while it waits.

24. **Choose a small brain.** Button 8 → pick the smallest one (marked **Very small** or **Fast**). Use the photo brain only when you need it, then switch back.
25. **Dim the screen.** Press the keyboard key with a small sun (often on the top row, sometimes together with the **Fn** key), or click the top-right corner of the screen and drag the brightness slider down. Turn off the keyboard light if it has one.
26. **Turn off what you don't need.** Top-right corner of the screen: switch **Wi-Fi** and **Bluetooth** off (there is no internet anyway). Unplug USB devices you aren't using. **Keep the AI-ARK drive plugged in if the library or the brain comes from it** (Button 7 shows "Ark drive connected").
27. **Ask short questions.** Ask one clear question at a time. Read the library (button 4) instead of asking the AI when you can: reading uses almost no power.
28. **Close the lid only if it is plugged in.** Closing the lid does not turn it off, so it keeps using battery.

**Expected battery time.** Measure it once for this computer (during the quarterly check, [Appendix, Part B](#appendix-monthly-and-quarterly-checks), step B12) and write it here in pen:

| What is running | Screen | Hours |
|---|---|---|
| Normal brain, chatting often | bright | ____ h |
| Normal brain, chatting often | dimmed | ____ h |
| Smallest brain, a few questions per hour | dimmed | ____ h |
| Library and maps only, no chat | dimmed | ____ h |

Computer: __________  Measured on (date): __________

29. **Watch the warnings.** At 20 % the battery icon turns amber (`LAI-SVC-111`). At 10 % a red banner says "Battery very low. Plug in now or the computer will turn off." (`LAI-SVC-112`). Save what you are reading, write important answers on paper, and charge.

---

## Part 5. Charging from solar or a car (general advice)

<!-- runbook-lint: no-expect-section (advice) -->

These are general safety rules. They are not a full electrical guide.

30. **Know your charger's watts.** Read the label on the laptop charger brick (for example "19.5 V, 6.92 A, 135 W"). Anything that powers it must give at least that many watts.
31. **From a car:**
   - Use a **pure sine wave inverter** rated above the charger's watts, plugged into the car's 12 V socket or wired to the battery as its manual says, then plug the normal charger into the inverter. Or use a **USB-C car charger** if your laptop charges by USB-C (many small laptops do; big gaming laptops usually don't charge fully that way).
   - **Run the engine only outdoors**, never in a closed garage (carbon monoxide kills). Don't drain the car battery with the engine off for long: you may not be able to start the car.
32. **From solar:**
   - A solar panel needs a **charge controller** and a **battery bank** (or a "power station" that has both built in). Charge the laptop from the battery bank's inverter or USB-C output, not straight from the panel.
   - Charge during the sunniest hours. Use the AI then, and use the library and maps (low power) at night.
   - Keep panels, cables and batteries dry. Don't connect anything while it's raining.
33. **Charge before you need it.** When power is available, charge to 100 % and keep the charger plugged in.

---

## Part 6. The AI-ARK drive: plug in, eject, keep safe

The **AI-ARK drive** is the portable copy of everything. It is **not encrypted**, so anyone who finds it can read it. That is on purpose, so it can be used in an emergency.

34. **Plug it in.** Use any USB port. Wait about 10 seconds.
   - **What you should see:** Button 7 shows "Ark drive connected". Brains, books and maps on the drive become available.
   - **If it doesn't work:** "plugged in but not mounted" (`LAI-ARK-005`): open the **Files** app and click **AI-ARK** on the left, or press **Mount** in the Local AI app. "needs to be set up" (`LAI-ARK-010`): it is a new, empty drive; see runbook 04.
35. **Copy content inside (optional, makes it faster and safer).** When the Portal shows a problem with the button **Copy from the AI-ARK drive** (for example "The library has no books"), press it. Nothing is deleted. Or, in a terminal:

   ```
   aictl ark import --wait
   ```

   - **What you should see:** the Portal says "Copying from the drive has started"; the terminal shows a progress line, then `✓`. This works with no internet.
   - **If it doesn't work:** `LAI-SVC-118` (no usable drive): plug the drive in, wait a minute, press the button again. `LAI-SVC-117` (too many tries): wait a few minutes.
36. **Before unplugging: eject.** Press **Eject** in the Local AI app. In a terminal, first find the drive's id (it looks like `ark-3f9c0a12`), then eject it:

   ```
   aictl ark drives
   aictl ark eject <root-id>
   ```

   - **What you should see:** "You can unplug the drive now."
   - **If it doesn't work:** "files are in use" (`LAI-ARK-009`): a brain is running from the drive. Switch brain first (button 8), then eject again.
37. **Keep it safe:**
   - Store it in a padded case, dry, cool, out of the sun, away from magnets.
   - Never pull it out while the computer is writing to it. If you did, plug it back in once so it can check itself (`LAI-ARK-007`).
   - If there are two drives, keep them in **different places**.
   - Plug it into a computer with internet at least once a month (overnight) so it stays up to date. Yellow after 30 days, red after 90 (`LAI-ARK-008`).
   - Don't store personal secrets on it.

---

## Part 7. Leaving Survival mode (when the emergency is over)

38. Leaving Survival mode needs the **admin PIN**. In the Local AI app, open the dashboard and choose **Online**, then type the PIN. Or, in a terminal (a user who belongs to the `localai-operators` group, as the desktop user does on a lab computer, is not asked for the PIN; anyone else is):

   ```
   aictl mode online
   ```

   - **What you should see:** `Mode online`. If the internet works, the node rejoins the lab within a minute.
   - **If it doesn't work:** forgot the PIN: runbook 07, section **"Forgot PIN"**.

---

## Part 8. When something fails

39. **First, always look at what is wrong.** Press **7 Status and battery** in the Portal (or in the Local AI app): each problem has a code and a plain fix, often with a button. Or press **Ctrl + Alt + T**, type `aictl`, press **Enter**, then press **2** (*Find and fix problems*). The typed command, which does the same:

   ```
   aictl doctor
   ```

   It prints each problem with a code in brackets and a line starting **→ Fix:**. Do what the fix line says, then look again.

40. **Plain fixes for common codes:**
   - `LAI-SVC-106` or `LAI-SVC-102` (no brain / the AI isn't answering): press **8 Switch brain** and choose one. Still broken: restart the computer and wait 2 minutes.
   - `LAI-LLM-021` (no brain could start at all): run `aictl restart llama`. The library, maps and manuals still work meanwhile.
   - `LAI-SVC-107` (new brain couldn't start): choose a smaller brain.
   - `LAI-SVC-001` (library has no books) or `LAI-SVC-030` (no map): plug in the AI-ARK drive, then press **Copy from the AI-ARK drive** in the Portal (on the Library or Maps screen, or under the problem on button 7). Or run `aictl ark import --wait`.
   - `LAI-SVC-002` (library not running): press **Restart the library** under the problem on button 7. Or run `aictl services restart kiwix`.
   - `LAI-SVC-011` (voice input stopped): press **Restart speech** under the problem on button 7. Or run `aictl services restart whisper`. `LAI-SVC-010` (voice input missing): press **Copy from the AI-ARK drive**. Meanwhile use Chat (button 1).
   - `LAI-SVC-116` ("Lost contact with the AI service"): wait a minute; the Portal tries again by itself. If it stays, restart the computer (step 42).
   - `LAI-SVC-103` (microphone blocked): allow the microphone in the browser (lock icon next to the address), or plug in a headset.
   - `LAI-SVC-111`, `LAI-SVC-112`, `LAI-HW-013`, `LAI-HW-014` (battery low): plug in the charger. See Parts 4 and 5.
   - `LAI-HW-009`, `LAI-HW-010`, `LAI-HW-011` (too hot): put the laptop on a hard, flat surface; don't block the vents.
   - `LAI-ARK-005` (drive not mounted): open the drive in the **Files** app.
   - `LAI-ARK-007` (drive pulled out without eject): plug it back in once; next time press **Eject** first.
   - `LAI-DSK-001` (app says this user isn't allowed): press **Allow this user** in the app and type the administrator password in the window that appears. Then press **Log out now** and log back in. Meanwhile the browser way (step 6) works. Or in a terminal: `sudo usermod -aG localai-operators $USER`, then log out and back in.
   - `LAI-DSK-002` (you were allowed but haven't logged in again): press **Log out now** in the app and log back in.
   - `LAI-DSK-004` or `LAI-CLI-001` (the Local AI service isn't running): press **Start agent** in the app and type your login password. Or in a terminal: `sudo systemctl start localai-agent`.
   - `LAI-DSK-005` (the app couldn't start the service): the app says in plain words what failed. Press **Start agent** once more; if it fails again, press **Restart the computer** in the app. Or in a terminal: `sudo aictl doctor`.
   - `LAI-DSK-007` (the service refused the app's login): press **Make a new token** in the app and type the administrator password; the app connects again by itself. Or in a terminal: `sudo aictl token rotate`.
   - `LAI-SEC-004` (too many wrong PIN tries): wait the time shown.
41. **Library only, without the AI service.** If everything else fails but the library program still runs, press **Open the library only** on the app's error page: the library opens in the web browser. Or open the browser at `http://127.0.0.1:8888/library/`. If the app says the library is not running either, go to step 42.
42. **If nothing works:** press **Restart the computer** on the app's error page and confirm with **Restart**, or use the top-right corner → power icon → **Restart**. Wait 2 minutes. Then try Part 2 again.

More: runbook 07 (Troubleshooting) has one section per problem, and a list of every code.

---

## Part 9. If this computer dies: use the AI-ARK drive somewhere else

The drive holds everything. Pick the first option that fits:

43. **Another AI computer is available:** plug the drive into it (Part 6). Its brains, books and maps appear there.
44. **Any computer with Ubuntu or another Linux:** plug in the drive, open it in the **Files** app, and double-click **START-HERE.html**. It opens in the browser with no internet and links to all manuals. To read the library without any AI software, open a terminal and type, one line at a time:

   ```
   cd /media/$USER/AI-ARK
   mkdir -p ~/kiwix
   tar -xzf localai/kit/tools/kiwix-tools/kiwix-tools_linux-x86_64-*.tar.gz -C ~/kiwix --strip-components=1
   ~/kiwix/kiwix-serve --port 8888 --library localai/kiwix/library.xml
   ```

   Then open `http://127.0.0.1:8888` in the browser. Leave the terminal open while you read.
45. **A computer with Windows or macOS:** they can't read the drive by themselves (it is formatted for Linux). If you have the Ubuntu USB stick, start that computer from it and choose **Try Ubuntu**, then do step 44.
46. **To make a new AI computer from scratch, with no internet:** follow runbook 06 (Rebuild a node offline). It's also on the drive: `localai/docs/06-rebuild-a-node-offline.html`.

**Every 3 months:** test all of this for real with the "pull the plug" checklist in the [Appendix, Part B](#appendix-monthly-and-quarterly-checks) at the end of this page. The result is stored on the computer with `sudo aictl drill record-manual`; use the paper log below only when that is not possible.

---

## Part 10. Paper log (only when the computer can't be used)

Normally every drill and problem is recorded on the computer itself (`aictl drill show` lists them; the quarterly drill is saved with `sudo aictl drill record-manual --result pass` or `--result fail --notes "…"`). If the computer is dead, has no power, or you can't log in, write it here in pen instead, and type it in later with `sudo aictl drill record-manual` once the computer works again.

| Date | Computer | What happened, or drill result (pass / fail) | Code shown (`LAI-…`) | What you did | Name |
|---|---|---|---|---|---|
| | | | | | |
| | | | | | |
| | | | | | |
| | | | | | |

---

## Appendix. Monthly and quarterly checks

An emergency computer that was never tested is a guess. Two checks keep this one honest: a **monthly check** that the computer runs by itself, and a **quarterly "pull the plug" check** that you do by hand, because software cannot really unplug a computer.

> 🛠 Verify when implemented (Epic E9 drills, `aictl drill`). Follow it once on a real node and fix anything that differs.

### Part A. Every month: the automatic check (nothing to do, but read the result)

A1. **Let it run.** On the first Sunday of every month, around 4:30 in the morning, the computer tests itself: each brain answers, the library searches, speech works both ways, maps draw, the manuals and the rebuild kit are there. It does **not** disconnect anything and does **not** change the mode. It does not run while the computer is in Survival mode or on battery below 50 %. If a sync is running, or the AI is busy answering you, it waits and tries again later.
   - **What you should see:** a notification, "Monthly check finished: everything works offline." Or, if it found something, "Monthly check found problems. Open Status."
   - **If it doesn't work:** no notification for more than a month: go to step A4.

A2. **Read the last result** (any time, nothing is changed). Press **Ctrl + Alt + T** and type:

   ```
   aictl drill show
   ```

   - **What you should see:** the date and result of the last check, and one line for each problem with its code (`LAI-DRL-…`) and a fix line.
   - **If it doesn't work:** "no drill has run yet": run one now with step A3. A code like `LAI-AGT-…` or `LAI-CLI-…` with a fix line: do what it says, see [Troubleshooting, step zero](07-troubleshooting.md).

A3. **Run it now, and fix what it finds.** Takes up to about 30 minutes; the computer stays usable. Type:

   ```
   aictl drill
   ```

   Do what each fix line says, then run `aictl drill` again. While the last check had problems, Status (button 7) shows `LAI-DRL-001` ("The last drill found problems"); it goes away by itself after the next check that passes.
   - **What you should see:** one line per test and a last line such as `✓ Drill passed`.
   - **If it doesn't work:** `LAI-DRL-002` (a sync is running), `LAI-DRL-003` (the computer is in Survival mode), `LAI-DRL-004` (the AI is in use) or `LAI-DRL-005` (battery below 50 %): it refused to start. Wait, finish what you were doing, or plug in the charger, then try again. More: [runbook 08, Part 5](08-scheduled-sync.md).

A4. **No check for 45 days.** Status shows `LAI-DRL-006` ("No drill in 45 days"). Run `aictl drill` (step A3), then check that the monthly timer is on:

   ```
   systemctl list-timers 'localai-drill.timer'
   ```

   - **What you should see:** one line with the time of the next check.
   - **If it doesn't work:** no line: turn it on with `sudo systemctl enable --now localai-drill.timer`. Still nothing: runbook [07 Troubleshooting](07-troubleshooting.md), index by code.

### Part B. Every 3 months: the "pull the plug" check (17 steps, about 45 minutes)

Do it every 3 months, by someone who is **not** the person who set up the computer, if possible. It tests what software cannot: really no network, really on battery, a real restart. Take this page and a pen.

Computer: __________  Date: __________  Done by: __________

B1. **Write down the time.** Run `aictl drill` first and check that it passes (`aictl drill show`).
   - **What you should see:** `✓ Drill passed`, or "passed with warnings".
   - **If it doesn't work:** fix what it lists before going on (step A3 above, [07 Troubleshooting](07-troubleshooting.md)). A check that refused to start (`LAI-DRL-002` to `LAI-DRL-005`): see step A3.

B2. **Unplug the network cable and turn Wi-Fi off:** top-right menu, Wi-Fi, Off. On the MSI laptop, also check that it does not start Windows by itself.
   - **What you should see:** the Wi-Fi icon is gone or says off; no network name is shown.
   - **If it doesn't work:** can't find the Wi-Fi switch: open **Settings**, then **Wi-Fi**, and switch it off there. If the computer still shows a network, unplug the router. See [07 Troubleshooting](07-troubleshooting.md).

B3. **Unplug the charger** (laptops). Battery: ______ %
   - **What you should see:** the battery icon shows it is discharging.
   - **If it doesn't work:** the icon still says charging: pull the plug from the laptop itself, not from the wall.

B4. **Restart the computer.** Log in.
   - **What you should see:** the Local AI app opens by itself, or you can open it from the dock.
   - **If it doesn't work:** [Part 1](#part-1-turn-it-on-and-log-in) and [Part 2](#part-2-start-survival-mode), steps 4 to 7.

B5. **Press Survival mode** in the app.
   - **What you should see:** the Portal fills the screen within 2 minutes. The top line says **Ready**, or lists problems with codes.
   - **If it doesn't work:** [Part 2](#part-2-start-survival-mode), step 6 (the browser way), then [Part 8](#part-8-when-something-fails).

B6. **Chat:** press **1** and ask "How do I treat a minor burn?".
   - **What you should see:** an answer in under 30 seconds.
   - **If it doesn't work:** `LAI-SVC-102` or `LAI-SVC-106`: [Part 8](#part-8-when-something-fails), step 40. See [07, Chat says error](07-troubleshooting.md).

B7. **Photo:** press **2**, take a photo of any object (or choose one from the Pictures folder) and ask "What is this?".
   - **What you should see:** a sensible description. The computer may switch brain by itself; that is fine.
   - **If it doesn't work:** press **8** and choose the brain marked **Sees photos**. See [07, Talk or photo doesn't work](07-troubleshooting.md).

B8. **Talk:** press **3**, ask a question aloud in Spanish, then in English.
   - **What you should see:** a spoken answer in the same language each time.
   - **If it doesn't work:** `LAI-SVC-010`, `LAI-SVC-011` or `LAI-SVC-103`: [Part 8](#part-8-when-something-fails), step 40.

B9. **Library:** press **4** and search "water purification". Open a result. Then press **F** and choose *Severe bleeding*.
   - **What you should see:** the article opens each time.
   - **If it doesn't work:** `LAI-SVC-001` or `LAI-SVC-002`: [Part 8](#part-8-when-something-fails), step 40.

B10. **Maps:** press **5**, search for your town, press **Nearest: hospital**.
   - **What you should see:** a map with streets and names, and the nearest hospital marked.
   - **If it doesn't work:** `LAI-SVC-030`: plug in the AI-ARK drive. See [07, Maps don't work](07-troubleshooting.md).

B11. **Manuals:** press **6** and open "01. Survival mode: the emergency guide". Print the Survival card, or check that the printed one is current (it names this computer and matches what button 6 shows today).
   - **What you should see:** the manual opens; the card prints on one page.
   - **If it doesn't work:** no printer: check the printed card instead. A card that is out of date: print a new one and throw the old one away.

B12. **Status:** press **7**. Battery: ______ %, time left: ______. Compare with step B3, and write the measured hours in the battery table of [Part 4](#part-4-saving-battery).
   - **What you should see:** a battery level lower than at step B3 and a time left that makes sense.
   - **If it doesn't work:** no battery reading: this computer may have no battery. See [07, Battery](07-troubleshooting.md).

B13. **Ark drive:** plug in the AI-ARK drive.
   - **What you should see:** Status shows "Ark drive connected". Open **START-HERE.html** on the drive from the **Files** app: it opens in the browser with no internet.
   - **If it doesn't work:** `LAI-ARK-005`: open the drive in the **Files** app. See [Part 6](#part-6-the-ai-ark-drive-plug-in-eject-keep-safe) and [07, The ark drive is not found](07-troubleshooting.md).

B14. **Without the app:** close the app, open a browser and go to `http://127.0.0.1:7070/portal`.
   - **What you should see:** the Portal, working the same.
   - **If it doesn't work:** `aictl mode survival` in a terminal ([Part 2](#part-2-start-survival-mode), step 7).

B15. **Rebuild check (optional, about 30 minutes, needs the admin):** type `sudo aictl rebuild-llama --from-kit --no-activate`. The kit supplies the source and `--no-activate` builds and tests without replacing the running program.
   - **What you should see:** it finishes with a `✓` line, with no internet.
   - **If it doesn't work:** `LAI-DRL-071` or the code shown: [runbook 06](06-rebuild-a-node-offline.md).

B16. **Plug the charger and the network back in.** Leave Survival mode: **Online** in the dashboard (needs the PIN), or `aictl mode online` ([Part 7](#part-7-leaving-survival-mode-when-the-emergency-is-over)).
   - **What you should see:** the mode shows **online**, and the cloud shows this computer again within a minute.
   - **If it doesn't work:** [Part 7](#part-7-leaving-survival-mode-when-the-emergency-is-over). See [07, Cloud shows node offline](07-troubleshooting.md).

B17. **Record the result on the computer** (needs `sudo` or the admin PIN). Do not write passwords or the PIN in the notes (2000 characters at most).

   ```
   sudo aictl drill record-manual --result pass --notes "battery 3.5 h, all buttons ok"
   ```

   Use `--result fail` if any step failed, and put the step numbers and the codes in `--notes`.
   - **What you should see:** a `✓` line, and `aictl drill show` now lists this manual check.
   - **If it doesn't work:** `LAI-CLI-008`: run it in a terminal (it asks for the PIN) or with `sudo`. If the computer can't be used at all, write the result in the paper log ([Part 10](#part-10-paper-log-only-when-the-computer-cant-be-used)) and record it here later.

Anything that failed: write down the code shown and follow its fix line, or look it up in [07 Troubleshooting](07-troubleshooting.md).

<div style="page-break-after: always"></div>

# 01. Modo supervivencia: la guía de emergencia

**Versión en español. The English version is above.**

Imprima esta página y guárdela junto a la computadora, con el disco AI-ARK. La versión corta de una página es la [tarjeta de supervivencia](00-survival-card.md).

> **Antes de empezar**
> - Necesita: la laptop de IA (o computadora de IA), su cargador, y el nombre y la contraseña de su usuario.
> - Ayuda tener: el disco AI-ARK (un disco portátil pequeño con la etiqueta **AI-ARK**), un ratón, audífonos con micrófono.
> - **No** necesita internet. **No** necesita el PIN de administrador para iniciar el modo supervivencia.
> - **Tiempo:** unos 5 minutos para empezar.

> 🛠 Verificar cuando esté implementado (Épicas E4 Portal de Supervivencia, E8 aplicación de escritorio). Las pantallas siguen el diseño; revise nombres y tiempos en una computadora real.

---

## Parte 1. Encenderla e iniciar sesión

1. **Conecte el cargador** si puede. Luego presione el botón de encendido una vez.
   - **Lo que debería ver:** el logo del fabricante y luego Ubuntu iniciando. Espere hasta 2 minutos.
   - **Si no funciona:** mantenga presionado el botón de encendido 10 segundos, suéltelo, espere 5 segundos y presiónelo una vez. Si sigue apagada: cárguela 30 minutos e intente otra vez.

2. **Si la pantalla pide una "frase de contraseña" o "contraseña del disco" antes de todo:** el disco de esta computadora está cifrado. Escriba esa frase y presione **Enter**.
   - **Lo que debería ver:** Ubuntu sigue iniciando.
   - **Si no funciona:** revise el idioma del teclado y Bloq Mayús. Sin esta frase nadie puede abrir el disco; use el disco AI-ARK en otra computadora (Parte 9).

3. **Inicie sesión.** Haga clic en el nombre de usuario (`<DESKTOP_USER>`), escriba su contraseña y presione **Enter**.
   - **Lo que debería ver:** el escritorio. Después de unos segundos la aplicación **Local AI** puede abrirse sola. Si la computadora ya estaba en modo supervivencia, la pantalla de botones grandes (el **Portal de Supervivencia**) ocupa toda la pantalla. En ese caso pase a la Parte 3.
   - **Si no funciona:** la contraseña no está escrita en esta página a propósito. Pregunte al dueño, o búsquela donde el laboratorio la guarda.

---

## Parte 2. Iniciar el modo supervivencia

4. **Abra la aplicación Local AI.** Si no está abierta: presione la tecla **Super** (la del logo de Windows), escriba `Local AI` y presione **Enter**.
   - **Lo que debería ver:** una ventana llamada **Local AI**.
   - **Si no funciona:** vaya al paso 6 (con el navegador).

5. **Presione Modo supervivencia.** Haga clic en el ícono pequeño de Local AI arriba en la pantalla (el menú de la bandeja), elija **Modo**, luego **⚠ Modo supervivencia**, y confirme con **Cambiar a modo supervivencia**. O, en la ventana, presione el botón **Iniciar modo supervivencia** (no pide confirmación).
   - **Lo que debería ver:** en unos 2 minutos el Portal de Supervivencia ocupa la pantalla. La línea de arriba dice **Listo**, o **Listo, con problemas**, con una lista corta.
   - **Si no funciona:** vaya al paso 6.

6. **Con el navegador (si la aplicación no abre).** Abra cualquier navegador web (Firefox está en la barra de la izquierda). Haga clic en la barra de direcciones de arriba y escriba exactamente:

   ```
   http://127.0.0.1:7070
   ```

   y presione **Enter**. Si ve una barra amarilla **El modo supervivencia está apagado**, presione **Iniciar modo supervivencia**.
   - **Lo que debería ver:** el Portal de Supervivencia con 8 botones grandes.
   - **Si no funciona:** vaya al paso 7.

7. **Con la terminal (si el navegador no muestra nada).** Presione **Ctrl + Alt + T**. Se abre una ventana de terminal. Escriba `aictl` y presione **Enter**. Se abre un menú. Presione **5** (Modo) y luego **4** (Supervivencia). Espere a que diga `Survival mode is on` (el modo supervivencia está activo), presione **Enter** para volver al menú y **q** para cerrarlo. Luego repita el paso 6.

   Lo mismo escrito en una sola línea, si lo prefiere:

   ```
   aictl mode survival
   ```

   - **Lo que debería ver:** líneas como `Starting the library and speech`, y luego `✓ Survival mode is on`.
   - **Si no funciona:** aparece un mensaje con un código como `[LAI-CLI-001]` y debajo una línea de solución. Haga lo que dice esa línea. Vea también la Parte 8 y el manual 07, sección **"The app won't open"** (la aplicación no abre).

---

## Parte 3. El Portal de Supervivencia, botón por botón

Puede hacer clic en un botón o presionar su número en el teclado. **Esc** siempre vuelve a la pantalla de inicio. El selector **English | Español** de arriba cambia el idioma. **A A+ A++** agranda el texto.

### La app Local AI en modo supervivencia (pantalla completa)

Cuando la computadora está en modo supervivencia, la app **Local AI** llena toda la pantalla con el Portal de Supervivencia. Una franja delgada naranja arriba dice **Modo supervivencia**. Tiene un selector **EN | ES** para el idioma y un botón **Salir de pantalla completa**.

- **Para hacer la ventana más pequeña (por ejemplo, para usar otro programa):** presione **Salir de pantalla completa** en la franja. La franja se queda y el botón ahora dice **Pantalla completa**: presiónelo para volver a llenar la pantalla. La tecla F11 hace lo mismo. Esto **no** apaga el modo supervivencia.
- **Si cierra la ventana:** la app sigue funcionando en segundo plano. Haga clic en el icono pequeño de Local AI arriba en la pantalla (la bandeja), o abra **Local AI** otra vez con la tecla Super: el Portal vuelve a llenar la pantalla. Para cerrar de verdad, use **Salir** en el menú de la bandeja. La computadora sigue en modo supervivencia aunque cierre la app.
- **El selector de idioma de la franja** cambia la franja y el Portal juntos. Por defecto el idioma sigue el idioma de la propia computadora.
- **Si la franja muestra un código como `LAI-DSK-001`** (este usuario no puede controlar Local AI): el Portal funciona igual, todo lo que tiene está disponible. El código le dice al dueño qué arreglar después: vea el runbook de solución de problemas.
- **Lo que debe ver:** el Portal con sus botones grandes bajo una franja de 1 línea. **Si no funciona:** use la vía del navegador (paso 6).

### Botón 1: Conversar (escribir preguntas)
8. Presione **1**. Después de un mensaje corto "Abriendo la conversación", se abre la página de conversación.
9. Haga clic en el cuadro de abajo, escriba su pregunta con palabras sencillas y presione **Enter**.
   - **Lo que debería ver:** la respuesta aparece palabra por palabra, normalmente en menos de 30 segundos.
   - **Para volver:** presione **Atrás** en el navegador, o escriba `127.0.0.1:7070/portal` en la barra de direcciones.
   - **Si no funciona:** "No hay ningún cerebro funcionando" → presione **Elegir un cerebro** (vea el botón 8). Códigos `LAI-SVC-102` o `LAI-SVC-106`: vea la Parte 8.

### Botón 2: Preguntar por una foto
10. Presione **2**. Presione **Tomar foto** (usa la cámara de la laptop) o **Elegir archivo** (una foto que ya está en la computadora).
11. Escriba una pregunta, o presione una pregunta rápida: **¿Qué es esto?**, **¿Es peligroso?**, **¿Cómo arreglo esto?**, **Lea el texto**. Presione **Preguntar**.
   - **Lo que debería ver:** si el cerebro actual no puede ver fotos, un cuadro ofrece cambiar a uno que sí puede. Presione **Cambiar** y espere cerca de un minuto. Luego aparece la respuesta. **Leer en voz alta** la dice en voz alta.
   - **Importante:** la IA puede equivocarse. Nunca coma ni toque una planta, hongo o animal solo porque la IA dice que es seguro.
   - **Si no funciona:** "En esta computadora no hay un cerebro que entienda fotos" (`LAI-SVC-040`): esta computadora no puede ver fotos; use Conversar y describa la cosa con palabras. "No se puede abrir esta foto" (`LAI-SVC-108`): pruebe con otra foto.

### Botón 3: Hablar (hablar y escuchar)
12. Presione **3**. La primera vez el navegador pide usar el micrófono: presione **Permitir**.
13. Presione **Espacio** (o haga clic en el micrófono grande), diga su pregunta y luego deje de hablar. Se detiene solo después de un silencio corto.
   - **Lo que debería ver:** "Usted dijo: …" con sus palabras, luego "Pensando …", y luego la respuesta se dice en voz alta y aparece escrita. **Dejar de hablar** la detiene; **Repetir** la dice otra vez. **Corregir** le deja arreglar las palabras antes de enviarlas.
   - **Si no funciona:** "El micrófono está bloqueado" (`LAI-SVC-103`): haga clic en el candado junto a la barra de direcciones y permita el micrófono, o conecte unos audífonos con micrófono. "No escuché nada": hable más cerca. Sin voz, solo texto (`LAI-SVC-022`): lea la respuesta en la pantalla.

### Botón 4: Biblioteca (Wikipedia, medicina, guías)
14. Presione **4**. Escriba lo que busca (por ejemplo `purificar agua`) y presione **Enter**.
15. Haga clic en un resultado para leerlo. **Volver a los resultados** regresa a la lista. La lista **Libros** a la derecha abre un libro (por ejemplo WikiMed para medicina, iFixit para reparaciones).
   - **Lo que debería ver:** una lista de artículos; el artículo se abre dentro del Portal.
   - **Si no funciona:** "La biblioteca no tiene libros" (`LAI-SVC-001`): conecte el disco AI-ARK, espere 10 segundos y presione el botón **Copiar desde la unidad AI-ARK** en esa pantalla. Dice "Comenzó la copia desde la unidad"; los libros aparecen solos cuando termina. "La biblioteca no está funcionando" (`LAI-SVC-002`): presione **Reiniciar la biblioteca**, o vea la Parte 8.

### Primeros auxilios (presione F)
16. En la pantalla de inicio, presione **F**, o haga clic en un botón de la fila **PRIMEROS AUXILIOS** (RCP, Atragantamiento, Sangrado grave, Quemaduras, Hueso roto, Mordedura de serpiente, **Más …**).
   - **Lo que debería ver:** el artículo se abre de inmediato.
   - **Esto no es un médico.** Si algún teléfono funciona, llame al número de emergencias que aparece en pantalla (911 en Costa Rica). Actúe primero, lea después.
   - **Si no funciona:** "El libro de medicina no está instalado": conecte el disco AI-ARK, o use la búsqueda de la Biblioteca.

### Botón 5: Mapas
17. Presione **5**. Escriba un pueblo o lugar en el cuadro de búsqueda y presione **Enter**.
18. **Dónde estoy** muestra su posición si hay un GPS conectado. Sin GPS, haga clic derecho (o mantenga presionado) en el mapa donde está y elija **Estoy aquí**.
19. **Lo más cercano: hospital · clínica · agua · pueblo** muestra los más cercanos a su posición.
   - **Lo que debería ver:** un mapa con calles y nombres. Funciona sin internet.
   - **Si no funciona:** "No hay un mapa sin internet instalado" (`LAI-SVC-030`): conecte el disco AI-ARK. El mapa no tiene nombres (`LAI-SVC-031`): el mapa funciona, solo faltan las etiquetas.

### Botón 6: Manuales
20. Presione **6**. Elija un manual: **01. Survival mode: the emergency guide** (esta página), **06. Rebuild a node with no internet**, **07. Troubleshooting** (los títulos de los manuales aparecen en inglés). **Imprimir la tarjeta de supervivencia** da un resumen de una página para imprimir (un PDF; en una computadora sin creador de PDF es una página web y se imprime desde el navegador: `LAI-CLI-014`, solo informativo). La tarjeta nunca muestra una contraseña, clave o dirección; si `sudo aictl docs render` se niega a hacerla, dice `LAI-CLI-016`.

### Botón 7: Estado y batería
21. Presione **7**.
   - **Lo que debería ver:** el nivel de batería y el tiempo que queda, qué cerebro está funcionando, el modo, el color del arca, el reloj, **Qué funciona** y **Problemas**. Cada problema tiene un código y una solución de una línea. Algunos problemas también tienen un botón que hace la solución por usted (por ejemplo **Reiniciar la biblioteca** o **Copiar desde la unidad AI-ARK**): presiónelo y lea la línea de abajo.
   - **Color del arca:** **Verde** = todo está aquí y al día. **Amarillo** = todo lo esencial está aquí. **Rojo** = falta algo esencial; lea los problemas.

### Botón 8: Cambiar cerebro
22. Presione **8**. Verá los cerebros que puede usar. Cada uno dice para qué sirve: **Rápido**, **Ve fotos**, **Muy pequeño … respuestas más lentas**.
23. Presione **Usar este** en el que quiera y confirme.
   - **Lo que debería ver:** "Cargando … 35 s". Cambiar tarda cerca de un minuto. Las preguntas en curso se detienen.
   - **Si no funciona:** "no pudo iniciar. Se reinició el cerebro anterior." (`LAI-SVC-107`): elija un cerebro más pequeño.

---

## Parte 4. Ahorrar batería

<!-- runbook-lint: no-expect-section (consejos) -->

Sin cargador, la batería es su combustible. La IA gasta mucha energía **mientras responde** y muy poca mientras espera.

24. **Elija un cerebro pequeño.** Botón 8 → elija el más pequeño (marcado **Muy pequeño** o **Rápido**). Use el cerebro de fotos solo cuando lo necesite y luego vuelva al otro.
25. **Baje el brillo de la pantalla.** Presione la tecla con un sol pequeño (casi siempre en la fila de arriba, a veces junto con la tecla **Fn**), o haga clic en la esquina superior derecha de la pantalla y baje la barra de brillo. Apague la luz del teclado si tiene.
26. **Apague lo que no necesita.** Esquina superior derecha de la pantalla: apague **Wi-Fi** y **Bluetooth** (de todos modos no hay internet). Desconecte los aparatos USB que no use. **Deje conectado el disco AI-ARK si la biblioteca o el cerebro vienen de él** (el botón 7 dice "Disco del arca conectado").
27. **Haga preguntas cortas.** Una pregunta clara a la vez. Cuando pueda, lea la biblioteca (botón 4) en vez de preguntarle a la IA: leer casi no gasta energía.
28. **Cierre la tapa solo si está conectada al cargador.** Cerrar la tapa no la apaga, así que sigue gastando batería.

**Tiempo de batería esperado.** Mídalo una vez para esta computadora (durante la revisión trimestral, [Apéndice, Parte B](#apéndice-revisiones-mensuales-y-trimestrales), paso B12) y escríbalo aquí con bolígrafo:

| Qué está funcionando | Pantalla | Horas |
|---|---|---|
| Cerebro normal, conversando seguido | brillante | ____ h |
| Cerebro normal, conversando seguido | con poco brillo | ____ h |
| Cerebro más pequeño, pocas preguntas por hora | con poco brillo | ____ h |
| Solo biblioteca y mapas, sin conversar | con poco brillo | ____ h |

Computadora: __________  Medido el (fecha): __________

29. **Ponga atención a los avisos.** Al 20 % el ícono de batería se pone ámbar (`LAI-SVC-111`). Al 10 % una franja roja dice "Batería muy baja. Conecte el cargador ya o la computadora se apagará." (`LAI-SVC-112`). Guarde lo que está leyendo, anote en papel las respuestas importantes, y cargue.

---

## Parte 5. Cargar con energía solar o con un carro (consejos generales)

<!-- runbook-lint: no-expect-section (consejos) -->

Estas son reglas generales de seguridad. No son una guía eléctrica completa.

30. **Conozca los vatios de su cargador.** Lea la etiqueta del cargador de la laptop (por ejemplo "19.5 V, 6.92 A, 135 W"). Lo que lo alimente debe dar por lo menos esos vatios.
31. **Desde un carro:**
   - Use un **inversor de onda senoidal pura** de más vatios que el cargador, conectado al enchufe de 12 V del carro o a la batería como diga su manual, y conecte el cargador normal al inversor. O use un **cargador USB-C de carro** si su laptop carga por USB-C (muchas laptops pequeñas sí; las laptops grandes para juegos casi nunca cargan completo así).
   - **Encienda el motor solo al aire libre**, nunca en un garaje cerrado (el monóxido de carbono mata). No descargue la batería del carro mucho tiempo con el motor apagado: puede que luego no arranque.
32. **Con energía solar:**
   - Un panel solar necesita un **controlador de carga** y un **banco de baterías** (o una "estación de energía" que ya trae ambos). Cargue la laptop desde el inversor o la salida USB-C del banco de baterías, no directo del panel.
   - Cargue en las horas de más sol. Use la IA en ese momento, y use la biblioteca y los mapas (poca energía) de noche.
   - Mantenga secos los paneles, cables y baterías. No conecte nada mientras llueve.
33. **Cargue antes de necesitarlo.** Cuando haya electricidad, cargue al 100 % y deje el cargador conectado.

---

## Parte 6. El disco AI-ARK: conectar, expulsar, cuidar

El **disco AI-ARK** es la copia portátil de todo. **No está cifrado**, así que cualquiera que lo encuentre puede leerlo. Es a propósito, para que se pueda usar en una emergencia.

34. **Conéctelo.** Use cualquier puerto USB. Espere unos 10 segundos.
   - **Lo que debería ver:** el botón 7 dice "Disco del arca conectado". Los cerebros, libros y mapas del disco quedan disponibles.
   - **Si no funciona:** "conectado pero no montado" (`LAI-ARK-005`): abra la aplicación **Archivos** y haga clic en **AI-ARK** a la izquierda, o presione **Montar** en la aplicación Local AI. "hay que prepararlo" (`LAI-ARK-010`): es un disco nuevo y vacío; vea el manual 04.
35. **Copiar el contenido a la computadora (opcional, más rápido y seguro).** Cuando el Portal muestre un problema con el botón **Copiar desde la unidad AI-ARK** (por ejemplo "La biblioteca no tiene libros"), presiónelo. No se borra nada. O, en una terminal:

   ```
   aictl ark import --wait
   ```

   - **Lo que debería ver:** el Portal dice "Comenzó la copia desde la unidad"; la terminal muestra una línea de progreso y luego `✓`. Funciona sin internet.
   - **Si no funciona:** `LAI-SVC-118` (no hay un disco utilizable): conecte el disco, espere un minuto y presione el botón otra vez. `LAI-SVC-117` (demasiados intentos): espere unos minutos.
36. **Antes de desconectarlo: expúlselo.** Presione **Expulsar** en la aplicación Local AI. En una terminal, primero busque el identificador del disco (se ve como `ark-3f9c0a12`) y luego expúlselo:

   ```
   aictl ark drives
   aictl ark eject <root-id>
   ```

   - **Lo que debería ver:** "Ya puede desconectar el disco."
   - **Si no funciona:** "hay archivos en uso" (`LAI-ARK-009`): hay un cerebro funcionando desde el disco. Primero cambie de cerebro (botón 8) y expulse otra vez.
37. **Cuídelo:**
   - Guárdelo en un estuche acolchado, seco, fresco, lejos del sol y de imanes.
   - Nunca lo desconecte mientras la computadora está escribiendo en él. Si pasó, conéctelo otra vez una vez para que se revise (`LAI-ARK-007`).
   - Si hay dos discos, guárdelos en **lugares distintos**.
   - Conéctelo a una computadora con internet por lo menos una vez al mes (durante la noche) para que se mantenga al día. Amarillo después de 30 días, rojo después de 90 (`LAI-ARK-008`).
   - No guarde secretos personales en él.

---

## Parte 7. Salir del modo supervivencia (cuando pase la emergencia)

38. Salir del modo supervivencia requiere el **PIN de administrador**. En la aplicación Local AI, abra el panel y elija **Online / En línea**, y escriba el PIN. O en una terminal (a un usuario del grupo `localai-operators`, como el usuario del escritorio en un computador del laboratorio, no se le pide el PIN; a los demás sí):

   ```
   aictl mode online
   ```

   - **Lo que debería ver:** `Mode online`. Si hay internet, la computadora vuelve al laboratorio en menos de un minuto.
   - **Si no funciona:** olvidó el PIN: manual 07, sección **"Forgot PIN"** (olvidé el PIN).

---

## Parte 8. Cuando algo falla

39. **Primero, siempre mire qué está mal.** Presione **7 Estado y batería** en el Portal (o en la app Local AI): cada problema trae un código y una solución sencilla, muchas veces con un botón. O presione **Ctrl + Alt + T**, escriba `aictl`, presione **Enter** y luego **2** (*Encontrar y arreglar problemas*). El comando escrito, que hace lo mismo:

   ```
   aictl doctor
   ```

   Muestra cada problema con un código entre corchetes y una línea que empieza con **→ Fix:** (solución). Haga lo que dice esa línea y vuelva a mirar. Para verlo en español: `aictl --lang es doctor`.

40. **Soluciones sencillas para los códigos más comunes:**
   - `LAI-SVC-106` o `LAI-SVC-102` (no hay cerebro / la IA no responde): presione **8 Cambiar cerebro** y elija uno. Si sigue mal: reinicie la computadora y espere 2 minutos.
   - `LAI-LLM-021` (ningún cerebro pudo iniciar): ejecute `aictl restart llama`. Mientras tanto la biblioteca, los mapas y los manuales siguen funcionando.
   - `LAI-SVC-107` (el cerebro nuevo no pudo iniciar): elija un cerebro más pequeño.
   - `LAI-SVC-001` (la biblioteca no tiene libros) o `LAI-SVC-030` (no hay mapa): conecte el disco AI-ARK y presione **Copiar desde la unidad AI-ARK** en el Portal (en la pantalla Biblioteca o Mapas, o debajo del problema en el botón 7). O ejecute `aictl ark import --wait`.
   - `LAI-SVC-002` (la biblioteca no está funcionando): presione **Reiniciar la biblioteca** debajo del problema en el botón 7. O ejecute `aictl services restart kiwix`.
   - `LAI-SVC-011` (se detuvo el reconocimiento de voz): presione **Reiniciar la voz** debajo del problema en el botón 7. O ejecute `aictl services restart whisper`. `LAI-SVC-010` (falta el reconocimiento de voz): presione **Copiar desde la unidad AI-ARK**. Mientras tanto use Conversar (botón 1).
   - `LAI-SVC-116` ("Se perdió contacto con el servicio de IA"): espere un minuto; el Portal lo intenta otra vez solo. Si sigue así, reinicie la computadora (paso 42).
   - `LAI-SVC-103` (micrófono bloqueado): permita el micrófono en el navegador (candado junto a la dirección), o conecte audífonos con micrófono.
   - `LAI-SVC-111`, `LAI-SVC-112`, `LAI-HW-013`, `LAI-HW-014` (batería baja): conecte el cargador. Vea las Partes 4 y 5.
   - `LAI-HW-009`, `LAI-HW-010`, `LAI-HW-011` (muy caliente): ponga la laptop sobre una superficie dura y plana; no tape las rejillas.
   - `LAI-ARK-005` (disco no montado): abra el disco en la aplicación **Archivos**.
   - `LAI-ARK-007` (disco desconectado sin expulsar): conéctelo otra vez una vez; la próxima vez presione **Expulsar** antes.
   - `LAI-DSK-001` (la aplicación dice que este usuario no tiene permiso): presione **Permitir este usuario** en la aplicación y escriba la contraseña de administrador en la ventana que aparece. Luego presione **Cerrar sesión ahora** y vuelva a entrar. Mientras tanto funciona el navegador (paso 6). O en una terminal: `sudo usermod -aG localai-operators $USER`, y luego cierre sesión y vuelva a entrar.
   - `LAI-DSK-002` (ya tiene permiso pero no ha vuelto a entrar): presione **Cerrar sesión ahora** en la aplicación y vuelva a entrar.
   - `LAI-DSK-004` o `LAI-CLI-001` (el servicio Local AI no está funcionando): presione **Start agent** (iniciar agente) en la aplicación y escriba su contraseña de inicio de sesión. O en una terminal: `sudo systemctl start localai-agent`.
   - `LAI-DSK-005` (la aplicación no pudo iniciar el servicio): la aplicación dice con palabras sencillas qué falló. Presione **Iniciar agente** una vez más; si vuelve a fallar, presione **Reiniciar la computadora** en la aplicación. O en una terminal: `sudo aictl doctor`.
   - `LAI-DSK-007` (el servicio rechazó el inicio de sesión de la aplicación): presione **Crear un token nuevo** en la aplicación y escriba la contraseña de administrador; la aplicación se conecta otra vez por sí sola. O en una terminal: `sudo aictl token rotate`.
   - `LAI-SEC-004` (demasiados intentos de PIN equivocados): espere el tiempo indicado.
41. **Solo la biblioteca, sin el servicio de IA.** Si todo lo demás falla pero el programa de la biblioteca sigue funcionando, presione **Abrir solo la biblioteca** en la página de error de la aplicación: la biblioteca se abre en el navegador. O abra el navegador en `http://127.0.0.1:8888/library/`. Si la aplicación dice que la biblioteca tampoco funciona, vaya al paso 42.
42. **Si nada funciona:** presione **Reiniciar la computadora** en la página de error de la aplicación y confirme con **Reiniciar**, o use la esquina superior derecha → ícono de apagado → **Reiniciar**. Espere 2 minutos. Luego intente la Parte 2 otra vez.

Más: el manual 07 (Troubleshooting, solución de problemas) tiene una sección por problema y una lista de todos los códigos.

---

## Parte 9. Si esta computadora se daña: use el disco AI-ARK en otra

El disco tiene todo. Elija la primera opción que le sirva:

43. **Hay otra computadora de IA disponible:** conecte el disco en ella (Parte 6). Sus cerebros, libros y mapas aparecen ahí.
44. **Cualquier computadora con Ubuntu u otro Linux:** conecte el disco, ábralo en la aplicación **Archivos** y haga doble clic en **START-HERE.html**. Se abre en el navegador sin internet, con enlaces a todos los manuales. Para leer la biblioteca sin ningún programa de IA, abra una terminal y escriba, una línea a la vez:

   ```
   cd /media/$USER/AI-ARK
   mkdir -p ~/kiwix
   tar -xzf localai/kit/tools/kiwix-tools/kiwix-tools_linux-x86_64-*.tar.gz -C ~/kiwix --strip-components=1
   ~/kiwix/kiwix-serve --port 8888 --library localai/kiwix/library.xml
   ```

   Luego abra `http://127.0.0.1:8888` en el navegador. Deje la terminal abierta mientras lee.
45. **Una computadora con Windows o macOS:** no pueden leer el disco por sí solas (está formateado para Linux). Si tiene la memoria USB de Ubuntu, inicie esa computadora desde ella, elija **Probar Ubuntu** y haga el paso 44.
46. **Para hacer una computadora de IA nueva desde cero, sin internet:** siga el manual 06 (Rebuild a node offline, reconstruir sin internet). También está en el disco: `localai/docs/06-rebuild-a-node-offline.html`.

**Cada 3 meses:** pruebe todo esto de verdad con la lista "desconectar todo" del [Apéndice, Parte B](#apéndice-revisiones-mensuales-y-trimestrales) al final de esta página. El resultado se guarda en la computadora con `sudo aictl drill record-manual`; use el registro en papel de abajo solo cuando eso no sea posible.

---

## Parte 10. Registro en papel (solo cuando la computadora no se puede usar)

Normalmente cada simulacro y cada problema queda registrado en la propia computadora (`aictl drill show` los muestra; el simulacro trimestral se guarda con `sudo aictl drill record-manual --result pass` o `--result fail --notes "…"`). Si la computadora está dañada, no tiene energía o no puede iniciar sesión, escríbalo aquí con bolígrafo, y regístrelo después con `sudo aictl drill record-manual` cuando la computadora vuelva a funcionar.

| Fecha | Computadora | Qué pasó, o resultado del simulacro (pasó / falló) | Código mostrado (`LAI-…`) | Qué hizo | Nombre |
|---|---|---|---|---|---|
| | | | | | |
| | | | | | |
| | | | | | |
| | | | | | |

---

## Apéndice. Revisiones mensuales y trimestrales

Una computadora de emergencia que nunca se probó es una apuesta. Dos revisiones la mantienen honesta: una **revisión mensual** que la computadora hace sola, y una **revisión trimestral "desconectar todo"** que usted hace a mano, porque ningún programa puede desconectar de verdad una computadora.

> 🛠 Verificar cuando esté implementado (Épica E9 simulacros, `aictl drill`). Sígalo una vez en una computadora real y corrija lo que sea distinto.

### Parte A. Cada mes: la revisión automática (no hay que hacer nada, pero lea el resultado)

A1. **Déjela correr.** El primer domingo de cada mes, hacia las 4:30 de la mañana, la computadora se prueba sola: cada cerebro responde, la biblioteca busca, la voz funciona en los dos sentidos, los mapas se dibujan, los manuales y el kit de reconstrucción están. **No** desconecta nada y **no** cambia el modo. No corre mientras la computadora está en modo supervivencia o con la batería por debajo del 50 %. Si hay una sincronización en curso, o la IA está ocupada respondiendo, espera y lo intenta más tarde.
   - **Lo que debería ver:** una notificación, "Revisión mensual terminada: todo funciona sin internet." O, si encontró algo, "La revisión mensual encontró problemas. Abra Estado."
   - **Si no funciona:** ninguna notificación en más de un mes: vaya al paso A4.

A2. **Lea el último resultado** (cuando quiera, no cambia nada). Presione **Ctrl + Alt + T** y escriba:

   ```
   aictl drill show
   ```

   - **Lo que debería ver:** la fecha y el resultado de la última revisión, y una línea por cada problema con su código (`LAI-DRL-…`) y una línea de solución.
   - **Si no funciona:** "ningún simulacro se ha ejecutado": haga uno ahora con el paso A3. Un código como `LAI-AGT-…` o `LAI-CLI-…` con una línea de solución: haga lo que dice, vea el [paso cero de Resolución de problemas](07-troubleshooting.md).

A3. **Ejecútela ahora y corrija lo que encuentre.** Tarda hasta unos 30 minutos; la computadora sigue siendo utilizable. Escriba:

   ```
   aictl drill
   ```

   Haga lo que diga cada línea de solución y vuelva a ejecutar `aictl drill`. Mientras la última revisión tenga problemas, Estado (botón 7) muestra `LAI-DRL-001` ("El último simulacro encontró problemas"); desaparece sola después de la siguiente revisión que pase.
   - **Lo que debería ver:** una línea por prueba y una última línea como `✓ Drill passed`.
   - **Si no funciona:** `LAI-DRL-002` (hay una sincronización en curso), `LAI-DRL-003` (la computadora está en modo supervivencia), `LAI-DRL-004` (la IA está en uso) o `LAI-DRL-005` (batería por debajo del 50 %): no quiso empezar. Espere, termine lo que hacía o conecte el cargador, y vuelva a intentarlo. Más: [manual 08, Parte 5](08-scheduled-sync.md).

A4. **Ninguna revisión en 45 días.** Estado muestra `LAI-DRL-006` ("Ningún simulacro en 45 días"). Ejecute `aictl drill` (paso A3) y compruebe que el temporizador mensual esté activo:

   ```
   systemctl list-timers 'localai-drill.timer'
   ```

   - **Lo que debería ver:** una línea con la hora de la próxima revisión.
   - **Si no funciona:** ninguna línea: actívelo con `sudo systemctl enable --now localai-drill.timer`. Sigue sin aparecer: manual [07 Resolución de problemas](07-troubleshooting.md), índice por código.

### Parte B. Cada 3 meses: la revisión "desconectar todo" (17 pasos, unos 45 minutos)

Hágala cada 3 meses, si es posible por alguien que **no** sea quien instaló la computadora. Prueba lo que un programa no puede: de verdad sin red, de verdad con batería, un reinicio de verdad. Lleve esta página y un bolígrafo.

Computadora: __________  Fecha: __________  Hecha por: __________

B1. **Anote la hora.** Primero ejecute `aictl drill` y compruebe que pasa (`aictl drill show`).
   - **Lo que debería ver:** `✓ Drill passed`, o "passed with warnings".
   - **Si no funciona:** corrija lo que liste antes de seguir (paso A3 de arriba, [07 Resolución de problemas](07-troubleshooting.md)). Una revisión que no quiso empezar (`LAI-DRL-002` a `LAI-DRL-005`): vea el paso A3.

B2. **Desconecte el cable de red y apague el Wi-Fi:** menú de arriba a la derecha, Wi-Fi, Apagar. En la laptop MSI, compruebe también que no inicie Windows por sí sola.
   - **Lo que debería ver:** el icono de Wi-Fi desaparece o dice apagado; no se muestra ningún nombre de red.
   - **Si no funciona:** no encuentra el interruptor de Wi-Fi: abra **Configuración**, luego **Wi-Fi**, y apáguelo ahí. Si la computadora aún muestra una red, desconecte el router. Vea [07 Resolución de problemas](07-troubleshooting.md).

B3. **Desconecte el cargador** (laptops). Batería: ______ %
   - **Lo que debería ver:** el icono de batería indica que se está descargando.
   - **Si no funciona:** el icono sigue diciendo que carga: saque el enchufe de la propia laptop, no de la pared.

B4. **Reinicie la computadora.** Inicie sesión.
   - **Lo que debería ver:** la aplicación Local AI se abre sola, o puede abrirla desde el dock.
   - **Si no funciona:** [Parte 1](#parte-1-encenderla-e-iniciar-sesión) y [Parte 2](#parte-2-iniciar-el-modo-supervivencia), pasos 4 a 7.

B5. **Presione Modo supervivencia** en la aplicación.
   - **Lo que debería ver:** el Portal llena la pantalla en menos de 2 minutos. La línea de arriba dice **Listo**, o enumera problemas con códigos.
   - **Si no funciona:** [Parte 2](#parte-2-iniciar-el-modo-supervivencia), paso 6 (por el navegador), y luego [Parte 8](#parte-8-cuando-algo-falla).

B6. **Conversar:** presione **1** y pregunte "¿Cómo trato una quemadura leve?".
   - **Lo que debería ver:** una respuesta en menos de 30 segundos.
   - **Si no funciona:** `LAI-SVC-102` o `LAI-SVC-106`: [Parte 8](#parte-8-cuando-algo-falla), paso 40. Vea [07, Chat says error](07-troubleshooting.md).

B7. **Foto:** presione **2**, tome una foto de cualquier objeto (o elija una de la carpeta Imágenes) y pregunte "¿Qué es esto?".
   - **Lo que debería ver:** una descripción razonable. La computadora puede cambiar de cerebro sola; está bien.
   - **Si no funciona:** presione **8** y elija el cerebro marcado **Ve fotos**. Vea [07, Talk or photo doesn't work](07-troubleshooting.md).

B8. **Hablar:** presione **3**, haga una pregunta en voz alta en español y luego en inglés.
   - **Lo que debería ver:** una respuesta hablada en el mismo idioma cada vez.
   - **Si no funciona:** `LAI-SVC-010`, `LAI-SVC-011` o `LAI-SVC-103`: [Parte 8](#parte-8-cuando-algo-falla), paso 40.

B9. **Biblioteca:** presione **4** y busque "water purification". Abra un resultado. Luego presione **F** y elija *Sangrado grave*.
   - **Lo que debería ver:** el artículo se abre cada vez.
   - **Si no funciona:** `LAI-SVC-001` o `LAI-SVC-002`: [Parte 8](#parte-8-cuando-algo-falla), paso 40.

B10. **Mapas:** presione **5**, busque su pueblo y presione **Lo más cercano: hospital**.
   - **Lo que debería ver:** un mapa con calles y nombres, y el hospital más cercano marcado.
   - **Si no funciona:** `LAI-SVC-030`: conecte el disco AI-ARK. Vea [07, Maps don't work](07-troubleshooting.md).

B11. **Manuales:** presione **6** y abra "01. Survival mode: the emergency guide". Imprima la tarjeta de supervivencia, o compruebe que la impresa esté al día (nombra esta computadora y coincide con lo que muestra hoy el botón 6).
   - **Lo que debería ver:** el manual se abre; la tarjeta se imprime en una página.
   - **Si no funciona:** sin impresora: revise la tarjeta impresa. Una tarjeta desactualizada: imprima una nueva y tire la vieja.

B12. **Estado:** presione **7**. Batería: ______ %, tiempo restante: ______. Compare con el paso B3 y anote las horas medidas en la tabla de batería de la [Parte 4](#parte-4-ahorrar-batería).
   - **Lo que debería ver:** un nivel de batería más bajo que en el paso B3 y un tiempo restante razonable.
   - **Si no funciona:** sin lectura de batería: puede que esta computadora no tenga batería. Vea [07, Battery](07-troubleshooting.md).

B13. **Disco del arca:** conecte el disco AI-ARK.
   - **Lo que debería ver:** Estado muestra "Disco del arca conectado". Abra **START-HERE.html** del disco desde la aplicación **Archivos**: se abre en el navegador sin internet.
   - **Si no funciona:** `LAI-ARK-005`: abra el disco en la aplicación **Archivos**. Vea la [Parte 6](#parte-6-el-disco-ai-ark-conectar-expulsar-cuidar) y [07, The ark drive is not found](07-troubleshooting.md).

B14. **Sin la aplicación:** cierre la aplicación, abra un navegador y vaya a `http://127.0.0.1:7070/portal`.
   - **Lo que debería ver:** el Portal, funcionando igual.
   - **Si no funciona:** `aictl mode survival` en una terminal ([Parte 2](#parte-2-iniciar-el-modo-supervivencia), paso 7).

B15. **Prueba de reconstrucción (opcional, unos 30 minutos, requiere al administrador):** escriba `sudo aictl rebuild-llama --from-kit --no-activate`. El kit aporta el código fuente y `--no-activate` compila y prueba sin reemplazar el programa en uso.
   - **Lo que debería ver:** termina con una línea `✓`, sin internet.
   - **Si no funciona:** `LAI-DRL-071` o el código que aparezca: [manual 06](06-rebuild-a-node-offline.md).

B16. **Conecte otra vez el cargador y la red.** Salga del modo supervivencia: **En línea** en el panel (pide el PIN), o `aictl mode online` ([Parte 7](#parte-7-salir-del-modo-supervivencia-cuando-pase-la-emergencia)).
   - **Lo que debería ver:** el modo muestra **online**, y la nube muestra de nuevo esta computadora en un minuto.
   - **Si no funciona:** [Parte 7](#parte-7-salir-del-modo-supervivencia-cuando-pase-la-emergencia). Vea [07, Cloud shows node offline](07-troubleshooting.md).

B17. **Registre el resultado en la computadora** (requiere `sudo` o el PIN de administrador). No escriba contraseñas ni el PIN en las notas (2000 caracteres como máximo).

   ```
   sudo aictl drill record-manual --result pass --notes "battery 3.5 h, all buttons ok"
   ```

   Use `--result fail` si algún paso falló, y ponga los números de paso y los códigos en `--notes`.
   - **Lo que debería ver:** una línea `✓`, y `aictl drill show` ahora muestra esta revisión manual.
   - **Si no funciona:** `LAI-CLI-008`: ejecútelo en una terminal (pide el PIN) o con `sudo`. Si la computadora no se puede usar en absoluto, escriba el resultado en el registro en papel ([Parte 10](#parte-10-registro-en-papel-solo-cuando-la-computadora-no-se-puede-usar)) y regístrelo aquí después.

Lo que haya fallado: anote el código que apareció y siga su línea de solución, o búsquelo en [07 Resolución de problemas](07-troubleshooting.md).
