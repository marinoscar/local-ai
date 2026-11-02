> **Imported source (archive, redacted).** Original: https://gist.github.com/marinoscar/608783eb8258175e91d58dedd5f46978 (laptop-server-setup.md)  
> Imported: 2026-11-02. Lab-specific values replaced with placeholders (see `inventory.example.yaml`).  
> This file is a historical record only. The maintained instructions are in [`docs/runbooks/`](../runbooks/) and [`docs/specs/`](../specs/).

# Laptop-as-a-Server Setup Guide

Written 2026-10-24 from a real setup session. Use it to repeat the setup on another laptop.

**Reference machine:** HP EliteBook 840 G6, i7-8665U, 14 GiB RAM, Intel AX200 Wi-Fi 6 (2x2), Ubuntu with GNOME (kernel 7.0.0-38-generic), hostname `laptop-ubuntu-01`.
**Goal:** the laptop stays on, keeps running with the lid closed, and is reachable over SSH and Tailscale.

---

## 1. Result in one paragraph

The power configuration works: the lid, sleep and idle settings are all in place, and the machine stayed up and reachable with the lid closed. Two things caused trouble: restarting `systemd-logind` froze the login, and Wi-Fi power save made SSH feel frozen. The remaining weak spot is the 5 GHz Wi-Fi link, which stalls now and then in every lid position. An Ethernet cable is the planned fix.

---

## 2. Final configuration (replicate this)

### 2.1 Ignore lid, power-key and idle actions (systemd-logind)

File `/etc/systemd/logind.conf.d/99-server.conf`:

```ini
[Login]
HandleLidSwitch=ignore
HandleLidSwitchExternalPower=ignore
HandleLidSwitchDocked=ignore
HandlePowerKey=ignore
HandleSuspendKey=ignore
HandleHibernateKey=ignore
IdleAction=ignore
```

Create it with:

```bash
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

**Apply it by REBOOTING, not by restarting logind** (see section 4, problem 1).

### 2.2 Mask all sleep targets

```bash
sudo systemctl mask sleep.target suspend.target hibernate.target hybrid-sleep.target
```

Verify: `systemctl is-enabled sleep.target suspend.target hibernate.target hybrid-sleep.target` should print `masked` four times.

### 2.3 Turn off GNOME idle suspend (run as the desktop user, not root)

```bash
gsettings set org.gnome.settings-daemon.plugins.power sleep-inactive-ac-type 'nothing'
gsettings set org.gnome.settings-daemon.plugins.power sleep-inactive-battery-type 'nothing'
```

Why: GNOME defaults to `suspend` on idle. The masked targets would block it anyway, but this removes the dependency. The screen still blanks and locks after about 5 minutes, which is harmless.

### 2.4 Performance power profile

```bash
powerprofilesctl set performance
powerprofilesctl get              # performance
cat /sys/devices/system/cpu/cpu0/cpufreq/energy_performance_preference   # performance
```

Note: with `intel_pstate`, `scaling_governor` still reads `powersave`. That is normal. The energy-performance preference is what changes.

### 2.5 Wi-Fi power save OFF, and keep it off

Wi-Fi power save made SSH stall (see section 4, problem 2). Three layers are needed, because each one alone proved unreliable:

```bash
# (a) NetworkManager default for new connections
sudo tee /etc/NetworkManager/conf.d/wifi-powersave-off.conf <<'EOF'
[connection]
wifi.powersave = 2
EOF
sudo nmcli general reload conf

# (b) Dispatcher script: re-applies it on every connect
sudo tee /etc/NetworkManager/dispatcher.d/90-wifi-powersave-off <<'EOF'
#!/bin/sh
# Keep Wi-Fi power save off (server use)
IFACE="$1"; ACTION="$2"
case "$IFACE" in
  wl*) case "$ACTION" in up|dhcp4-change|reapply) /usr/sbin/iw dev "$IFACE" set power_save off ;; esac ;;
esac
EOF
sudo chown root:root /etc/NetworkManager/dispatcher.d/90-wifi-powersave-off
sudo chmod 755 /etc/NetworkManager/dispatcher.d/90-wifi-powersave-off

# (c) Apply right now
sudo iw dev <wifi-interface> set power_save off
```

Replace `<wifi-interface>` with yours (it was `wlp58s0`; find it with `iw dev`).

Ubuntu ships `/etc/NetworkManager/conf.d/default-wifi-powersave-on.conf` with `wifi.powersave = 3`. Our file name sorts after it, so ours wins.

Verify: `iw dev <wifi-interface> get power_save` should print `Power save: off`. Check it again after a forced reconnect and after a reboot.

### 2.6 Remote access

Services (all were active and enabled at boot):

```bash
sudo apt install -y openssh-server
sudo systemctl enable --now ssh
curl -fsSL https://tailscale.com/install.sh | sh
sudo tailscale up
systemctl is-enabled ssh tailscaled NetworkManager     # enabled x3
```

- Plain OpenSSH was used, and Tailscale's own SSH was turned off (`sudo tailscale set --ssh=false`).
- Command history shows `sudo ufw allow in on tailscale0` was run. **`ufw` is currently inactive** on the reference machine, so nothing filters inbound traffic. Decide on purpose whether you want it on.
- `ssh` and `tailscaled` are system services, so they start at boot **without anyone logging in**. Anything that needs your GNOME session does not.

### 2.7 Housekeeping

- Unattended upgrades do not reboot automatically (`Automatic-Reboot` is commented out). Kernel updates wait for a manual reboot.
- Set the BIOS option **"Power on after AC restore"** so the machine comes back after a power loss. This was recommended but **not checked** on the reference machine.

---

## 3. Verification checklist (run on the new laptop)

```bash
# Lid and idle actions
systemd-analyze cat-config systemd/logind.conf | grep -E 'Lid|IdleAction|Key'
# Sleep targets masked
systemctl is-enabled sleep.target suspend.target hibernate.target hybrid-sleep.target
# GNOME idle suspend off
gsettings get org.gnome.settings-daemon.plugins.power sleep-inactive-ac-type
gsettings get org.gnome.settings-daemon.plugins.power sleep-inactive-battery-type
# Profile
powerprofilesctl get
# Wi-Fi power save
iw dev <wifi-interface> get power_save
# Power source (must be plugged in!)
cat /sys/class/power_supply/AC/online      # 1
# Services
systemctl is-active ssh tailscaled NetworkManager
systemctl --failed
# Anything holding the machine back?
systemd-inhibit --list --no-pager
```

Then the real lid test, below.

---

## 4. What went wrong, and what we learned

### Problem 1: login froze after the setup script

- **Symptom:** after running the script, the session logged out. Logging back in froze, and the laptop had to be hard-powered off.
- **Evidence:** the previous boot's journal showed a new `systemd-logind` process starting at the moment the config file was written, followed by GNOME Shell errors `NotInControl: You are not in control of this session`.
- **Most likely cause:** the script restarted `systemd-logind` while a graphical session was logged in. That is known to break the session. I could not prove this was the only factor.
- **After the reboot the same config loaded cleanly.**
- **Lesson:** after writing `99-server.conf`, **reboot**. If you need to apply it without a reboot, do it from an SSH session or text console, never from the desktop. A reload without restart (`sudo systemctl kill -s HUP systemd-logind`) is a possible alternative. **I did not test it on this machine.**

### Problem 2: SSH connected but was unresponsive

- **First test:** the lid closed for about 3 minutes. A heartbeat log showed no gaps, no suspend and no reboot, and `sshd` accepted the login. So the power setup worked. The problem was the network.
- **Wi-Fi power save was ON,** and it silently came back on after I reconnected the interface (`nmcli connection up` resets it). Searches showed this is a known AX200 issue (stalls and high latency with power save on). That is why section 2.5 uses three layers.
- Tailscale logs showed the path to the client flapping between direct and relayed routes, with repeated "UDP is blocked" messages.

### Problem 3: Wi-Fi stalls (the remaining issue)

Measured on 5 GHz, MarinWiFi (a mesh with at least 4 access points):

- Router ping: sometimes 90% loss, sometimes 1 ms, sometimes replies delayed 1 to 4 seconds with no loss.
- Download bursts of 135 to 223 Mbit/s followed by 0 to 30 Mbit/s.
- **These stalls happened with the lid open, with the lid closed, and with the lid closed inside a metal case.** So the lid is not the cause.
- Pinned to the 2.4 GHz access point in one test: 5 ms average, no stalls, stronger signal (-57 dBm vs -64). This is a **fallback option, not applied**.
- The ruled-out causes: the lid, the metal case, power save (off during later tests), background Wi-Fi scans, and AX200 firmware errors (none logged).
- Open suspects: roaming between mesh access points (a [known AX200 bug](https://bugzilla.kernel.org/show_bug.cgi?id=215660) after roaming), interference on 5 GHz, and the AX200 driver on 802.11ax (`iwlwifi disable_11ax=y` isolates it but slows the link).
- **Plan: use an Ethernet cable.** Wired removes all of this.

### Antennas and the closed lid

- On this model the Wi-Fi antennas are believed to be in the display. I could not find HP's antenna diagram, and other laptops' forum reports agree (see below).
- Measured penalty: about **5.5 dB** (average -63 dBm open vs -69 dBm closed). The link rate dropped from 1297/648 to 816/408 Mbit/s (rx/tx) at one point.
- That is real but moderate and not the cause of the stalls. It matters more if your signal is weak to begin with.
- On the new laptop, **measure it yourself** (section 5) rather than assuming.

---

## 5. How to test on the new laptop

### 5.1 Lid-closed heartbeat (proves it does not sleep)

Start it, then close the lid for 5 to 10 minutes, reopen, and read the log:

```bash
nohup bash -c 'while true; do echo "$(date +%T) up=$(cut -d" " -f1 /proc/uptime) lid=$(awk "{print \$2}" /proc/acpi/button/lid/*/state)"; sleep 10; done' >> ~/lid-test.log 2>&1 &
```

Pass: `lid=closed` rows appear, the timestamps are about 10 seconds apart with **no gaps**, and `uptime -s` is unchanged.

```bash
journalctl -b -g 'lid|suspend|Suspending' --no-pager | tail     # no "Suspending" line
```

### 5.2 Wi-Fi quality logger (signal, retries, router latency)

```bash
GW=$(ip route | awk '/default/{print $3; exit}'); IF=<wifi-interface>
nohup bash -c "while true; do s=\$(iw dev $IF station dump | awk '/signal:/{print \$2; exit}'); p=\$(ping -c1 -W1 $GW 2>/dev/null | awk -F'time=' '/time=/{print \$2}'); echo \"\$(date +%T) lid=\$(awk '{print \$2}' /proc/acpi/button/lid/*/state) signal=\${s}dBm gwping=\${p:-TIMEOUT} ps=\$(iw dev $IF get power_save | awk '{print \$3}')\"; sleep 5; done" > ~/lid-net.log 2>&1 &
```

Compare open and closed rows. A closed-lid drop of 10 dB or more points at the antenna. Frequent `TIMEOUT` rows or latency above 100 ms points at the link.

### 5.3 Quick speed and stall test (no install needed)

```bash
GW=$(ip route | awk '/default/{print $3; exit}')
ping -c 40 -i 0.2 -W 1 $GW | tail -2          # want 0% loss, max well under 100 ms
ping -c 40 -i 0.2 -W 1 1.1.1.1 | tail -2
for i in 1 2 3; do curl -s -o /dev/null -w "%{speed_download}\n" "https://speed.cloudflare.com/__down?bytes=25000000" | awk '{printf "down %.1f Mbit/s\n",$1*8/1e6}'; done
```

Watch the **maximum** ping time and the variation between runs, not just the average. A single-stream `curl` undershoots a fast connection, so use it to spot stalls, not to measure peak speed.

### 5.4 Final test

Close the lid, then SSH in from **another device on the network** (use the Tailscale name or IP). Run a few commands and check the session feels normal.

---

## 6. Checklist for the new laptop

1. Plug it in. **A discharging battery means the server dies when it empties** (the reference machine was on battery at the start).
2. Install `openssh-server` and Tailscale (section 2.6).
3. Apply section 2.1, 2.2, 2.3, 2.4.
4. Apply the Wi-Fi power-save fix (section 2.5) and verify it after a reconnect.
5. **Reboot** (not a logind restart).
6. Run the checklist in section 3.
7. Run the lid test in section 5.1, then check Wi-Fi quality (section 5.2 and 5.3).
8. Plan for Ethernet. If it must be Wi-Fi, consider locking to one stable band and a USB Wi-Fi adapter on a short cable.
9. BIOS: enable "Power on after AC restore", and check any battery-charge limit option.
10. Decide on `ufw` and on automatic reboots after updates.

---

## 7. Things to clean up on the reference machine

- **Passwordless sudo is still active.** Remove it:
  ```bash
  sudo rm /etc/sudoers.d/99-temp-nopasswd
  sudo -k && sudo -n true 2>&1 | head -1      # should now say a password is required
  ```
- Stop the test loggers and delete their files when finished: `pkill -f 'lid-net.log'; pkill -f 'lid-test.log'; rm -f ~/lid-test.log ~/lid-net.log ~/lid-test2.log`.
- The Wi-Fi connection is currently unpinned (no BSSID set). Verify with: `nmcli -g 802-11-wireless.bssid connection show MarinWiFi` (empty means unpinned).

---

## 8. Open items

- The original setup script is not in this file. The configuration above was reconstructed from the resulting system state (`99-server.conf`, masked targets), not from the script.
- The ufw rules (`allow in on tailscale0`) were typed in earlier, but ufw is inactive now.
- Not tested: reloading logind with `SIGHUP` instead of rebooting, the BIOS "power on after AC restore" setting, behavior after a real power loss, and the 2.4 GHz band for longer periods.
- Not done: the Ethernet test. If the cable fixes the stalls, the Wi-Fi diagnosis is confirmed.

---

## 9. Sources consulted

- [HP EliteBook 840 G6 Maintenance and Service Guide](https://manualsnet.com/hp/elitebook-840-g6) (no antenna diagram found in the excerpt)
- [Dell: Wi-Fi when the lid is closed](https://www.dell.com/community/General/e6410-wifi-doesn-t-connect-when-docked/td-p/3652869)
- [Framework: Wi-Fi in clamshell mode](https://community.frame.work/t/my-wifi-download-speed-is-substantially-reduced-when-using-my-framework-in-clamshell-has-anyone-else-experienced-this/56241)
- [HP community: Wi-Fi slow when closing the lid](https://h30434.www3.hp.com/t5/Gaming-Notebooks/WIFI-slow-when-closing-lid-screen/m-p/6509147)
- [Intel community: AX200 unusable unless power save is disabled](https://community.intel.com/t5/Wireless/WiFi-6-problem-with-Intel-AX200-in-Linux/m-p/1583827)
- [Arch forum: AX200 and disable_11ax](https://bbs.archlinux.org/viewtopic.php?id=274509)
- [Kernel bug 215660: AX200 rx lockups after roaming](https://bugzilla.kernel.org/show_bug.cgi?id=215660)
