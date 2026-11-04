# 09. A laptop as a server

Use this when a laptop must work like a server: stay on with the lid closed, never sleep, keep its Wi-Fi awake, come back after a power cut, and be reachable over SSH and Tailscale. This is the maintained version of [source/laptop-server-setup.md](../source/laptop-server-setup.md).

> **Before you start**
> - **The installer does all of this for you** on laptops ([installer.md §4.3 P4](../specs/installer.md#p4-laptop-hardening-laptop-hardening)). Normally you only need **Part 1** (verify) and **Part 3** (lid test). Part 2 is for fixing one setting by hand.
> - You need: the laptop, plugged in, a terminal (**Ctrl + Alt + T**), `sudo`.
> - **Time:** verify 5 minutes; lid test 15 minutes; manual setup 20 minutes plus a reboot.

> 🛠 Verify when implemented (Epic E5 for the automatic part). The manual commands below were proven on a real laptop (2026-10-24).

> ## ⚠️ Two warnings
> 1. **Never restart `systemd-logind`** to apply the lid settings (`systemctl restart systemd-logind`, or a `kill … systemd-logind`). Doing that from the desktop froze the login on the reference laptop and needed a hard power-off. **Reboot instead.**
> 2. **A laptop on battery is a server that will die.** Keep it plugged in. When the battery is empty, it stops.

**Words used on this page**
- **logind:** the part of Ubuntu that decides what happens when you close the lid or press the power button.
- **Sleep targets:** the system states "sleep", "suspend" and "hibernate". "Masked" means switched off completely.
- **Power profile:** Ubuntu's *Power Saver / Balanced / Performance* setting.

---

## Part 1. Verify (after the installer, and after any big update)

1. **Quick check with the doctor:**

   ```
   aictl doctor --area HW --area NET
   ```

   - **What you should see:** no `LAI-HW-020` (lid will sleep), `LAI-NET-014` (Wi-Fi power saving), `LAI-HW-015` (power profile) or `LAI-INS-040` to `LAI-INS-044`.
   - `LAI-INS-070` (info) means the installer changed the lid settings and a reboot is still needed; it goes away by itself after the next reboot, and `LAI-INS-040` is only checked after that.
   - You can also run `sudo /opt/localai/bin/aiagent setup harden --check`: it prints every problem with its code and changes nothing.
   - **If it doesn't work:** run the repair, then **reboot**:

     ```
     sudo bash /opt/localai/installers/install-node.sh --repair
     sudo reboot
     ```

     (If that file is missing, use the copy on the AI-ARK drive: `sudo bash /media/$USER/AI-ARK/localai/kit/installers/install-node.sh --from-ark /media/$USER/AI-ARK --repair`.)

2. **Each setting by hand.** Run each command; compare with "What you should see".

   | Setting | Command | What you should see |
   |---|---|---|
   | Lid and power keys ignored | `systemd-analyze cat-config systemd/logind.conf \| grep -E 'Lid\|IdleAction\|Key'` | `HandleLidSwitch=ignore`, `HandleLidSwitchExternalPower=ignore`, `HandleLidSwitchDocked=ignore`, `HandlePowerKey=ignore`, `HandleSuspendKey=ignore`, `HandleHibernateKey=ignore`, `IdleAction=ignore` (code `LAI-INS-040` if not) |
   | Sleep switched off | `systemctl is-enabled sleep.target suspend.target hibernate.target hybrid-sleep.target` | `masked` four times (`LAI-INS-041`) |
   | GNOME idle suspend off (run as your user, **not** with sudo) | `gsettings get org.gnome.settings-daemon.plugins.power sleep-inactive-ac-type` and the same with `sleep-inactive-battery-type` | `'nothing'` twice (`LAI-INS-042`) |
   | Performance power profile | `powerprofilesctl get` | `performance` (`LAI-INS-043`). On Intel, `cat /sys/devices/system/cpu/cpu0/cpufreq/energy_performance_preference` → `performance`. `scaling_governor` showing `powersave` is normal; don't "fix" it |
   | Wi-Fi power saving off | `iw dev` (find the name starting `wl`, for example `wlp58s0`), then `iw dev <wifi-interface> get power_save` | `Power save: off` (`LAI-INS-044`). Check again after a reconnect and after a reboot |
   | Plugged in | `cat /sys/class/power_supply/AC*/online` | `1` |
   | Remote access | `systemctl is-active ssh tailscaled NetworkManager` | `active` three times |
   | Nothing blocking | `systemctl --failed` and `systemd-inhibit --list --no-pager` | no failed units; no unexpected "sleep" blockers |

   - The screen still turns off and locks after about 5 minutes. That's harmless; the computer keeps running.

---

## Part 2. Apply a setting by hand

Use this only if the installer can't run, or one setting is wrong. These are exactly what the installer writes.

3. **Ignore the lid, power key and idle** (logind). Create the file:

   ```
   sudo mkdir -p /etc/systemd/logind.conf.d
   sudo tee /etc/systemd/logind.conf.d/99-server.conf <<'EOF'
   [Login]
   HandleLidSwitch=ignore
   HandleLidSwitchExternalPower=ignore
   HandleLidSwitchDocked=ignore
   HandlePowerKey=ignore
   HandleSuspendKey=ignore
   HandleHibernateKey=ignore
   IdleAction=ignore
   EOF
   ```

   ⚠️ **Don't restart logind.** It takes effect after the reboot in step 9.
4. **Switch sleep off** (safe to do live):

   ```
   sudo systemctl mask sleep.target suspend.target hibernate.target hybrid-sleep.target
   ```

   - **What you should see:** four lines `Created symlink … → /dev/null`.
5. **GNOME idle suspend off.** Run as **your own user** (no `sudo`):

   ```
   gsettings set org.gnome.settings-daemon.plugins.power sleep-inactive-ac-type 'nothing'
   gsettings set org.gnome.settings-daemon.plugins.power sleep-inactive-battery-type 'nothing'
   ```

6. **Performance power profile:**

   ```
   powerprofilesctl set performance
   powerprofilesctl get
   ```

   - **What you should see:** `performance`. It stays after reboots.
7. **Wi-Fi power saving off, in three layers** (each alone proved unreliable; reconnecting the Wi-Fi turns it back on):

   ```
   # (a) NetworkManager default for every connection
   sudo tee /etc/NetworkManager/conf.d/wifi-powersave-off.conf <<'EOF'
   [connection]
   wifi.powersave = 2
   EOF
   sudo nmcli general reload conf

   # (b) re-apply on every connect
   sudo tee /etc/NetworkManager/dispatcher.d/90-wifi-powersave-off <<'EOF'
   #!/bin/sh
   # Keep Wi-Fi power save off (server use). Installed by Local AI.
   IFACE="$1"; ACTION="$2"
   case "$IFACE" in
     wl*) case "$ACTION" in up|dhcp4-change|reapply) /usr/sbin/iw dev "$IFACE" set power_save off ;; esac ;;
   esac
   EOF
   sudo chown root:root /etc/NetworkManager/dispatcher.d/90-wifi-powersave-off
   sudo chmod 755 /etc/NetworkManager/dispatcher.d/90-wifi-powersave-off

   # (c) right now
   sudo iw dev <wifi-interface> set power_save off
   ```

   Replace `<wifi-interface>` with yours (from `iw dev`). Ubuntu's own file `default-wifi-powersave-on.conf` sets the opposite; ours sorts after it, so ours wins.
8. **Remote access: SSH and Tailscale.**

   ```
   sudo apt install -y openssh-server
   sudo systemctl enable --now ssh
   ```

   No internet: install it from the AI-ARK drive's kit instead of the first line: `sudo aictl kit install-debs --packages openssh-server`.

   Tailscale is installed by the Local AI installer from its signed package repository (not `curl … | sh`). Join the tailnet with `sudo tailscale up --ssh=false` ([05 Part 5](05-add-a-node.md)). Plain OpenSSH is used; Tailscale's own SSH stays off.
   - `ssh` and `tailscaled` start at boot **without anyone logging in**.
   - **Firewall (`ufw`):** the installer leaves it as it is. Decide on purpose. If you turn it on, allow SSH first (`sudo ufw allow OpenSSH`) or you may lock yourself out of remote access.
9. **Reboot:**

   ```
   sudo reboot
   ```

   Then run Part 1 again.

---

## Part 3. Things software can't set

10. **BIOS: "Power on after AC restore"** (sometimes called "AC Power Recovery", "Restore on AC power loss" or "Wake on AC"). Restart, press the BIOS key at the logo (often **F10** on HP, **F2** on Dell and MSI, **Del** on desktops), find the option under *Power* or *Advanced*, set it **On**, save and exit. Not every laptop has it. Then the laptop starts by itself when power comes back after a cut.
    - With disk encryption (LUKS), it still stops at the passphrase prompt after a power cut. That's the trade-off ([05 Part 1](05-add-a-node.md)).
11. **Battery charge limit:** if the BIOS or the maker's tool offers a limit (for example 80 %), turn it on for a laptop that is always plugged in. The battery lasts longer.
12. **Ethernet:** use a cable whenever you can. Wi-Fi on the reference laptop still stalled now and then, with the lid open or closed, even with power saving off. A cable removes the problem.
13. **Automatic reboots after updates:** leave them off (Ubuntu's default). Kernel updates then wait for a reboot you choose. After such a reboot, check the GPU still works ([07 → GPU not detected after update](07-troubleshooting.md#gpu-not-detected-after-update)).
14. **Airflow:** with the lid closed, put the laptop on a hard surface or a stand, never on cloth. Heat warnings: `LAI-HW-009`, `LAI-HW-010`, `LAI-HW-011`.

---

## Part 4. Test it

15. **Lid test** (proves it doesn't sleep). Start a heartbeat log, close the lid for 5 to 10 minutes, open it, and read the log:

    ```
    nohup bash -c 'while true; do echo "$(date +%T) up=$(cut -d" " -f1 /proc/uptime) lid=$(awk "{print \$2}" /proc/acpi/button/lid/*/state)"; sleep 10; done' >> ~/lid-test.log 2>&1 &
    ```

    After reopening the lid:

    ```
    tail -n 40 ~/lid-test.log
    journalctl -b -g 'lid|suspend|Suspending' --no-pager | tail
    ```

    - **What you should see:** rows with `lid=closed`, about 10 seconds apart with **no gaps**, and no `Suspending` line.
    - **If it doesn't work:** gaps or `Suspending`: Part 1 again, then reboot.
16. **Remote test:** close the lid, then from **another device** SSH in by its Tailscale name (`ssh <user>@<machine>`). Run a few commands; the session should feel normal. On the laptop's node, also check `https://ai-nN.marin.cr/health` answers `200` from outside.
17. **Wi-Fi quality** (only if you must use Wi-Fi):

    ```
    GW=$(ip route | awk '/default/{print $3; exit}')
    ping -c 40 -i 0.2 -W 1 $GW | tail -2
    ```

    - **What you should see:** `0% packet loss` and a **maximum** well under 100 ms. Frequent losses or second-long delays: use Ethernet, or pin the connection to a single stable access point or band.
18. **Clean up** the test log when done:

    ```
    pkill -f 'lid-test.log'; rm -f ~/lid-test.log
    ```

---

## Undo (laptop back to normal)

19. To remove these settings, the installer can revert them (SSH, packages and users are left):

    ```
    sudo bash /opt/localai/installers/install-node.sh --uninstall --revert-hardening
    ```

    That also uninstalls Local AI (content and settings are kept unless you add `--purge-…`). To revert only one setting by hand: delete the file you created in Part 2 (or `sudo systemctl unmask …` for step 4), then reboot.
