#!/bin/sh
# Boots the ISO in QEMU and waits until the kiosk browser has loaded the
# IMAGen interface. Saves the serial log and a screenshot next to the ISO.
#
#   scripts/boot-test.sh dist/imagen-amd64.iso
#
# Needs: qemu-system-x86, xorriso, socat. Uses KVM when /dev/kvm exists.
# RES=2560x1600 makes the virtual screen report that resolution as its
# preferred mode (like a laptop panel); the default is 1280x800.
# POWEROFF=1 then ejects the boot medium, clicks Shut down (screen must be
# 1280x800) and saves a screenshot every few seconds while the PC shuts down
# (shutdown-NN.ppm).
set -eu
ISO=$(realpath "$1")
OUT=$(dirname "$ISO")/boot-test
TIMEOUT=${TIMEOUT:-420}
RES=${RES:-1280x800}
XRES=${RES%x*}
YRES=${RES#*x}
rm -rf "$OUT" && mkdir -p "$OUT"

echo "== boot menu entries"
xorriso -osirrox on -indev "$ISO" -extract /boot/grub/grub.cfg "$OUT/grub.cfg" >/dev/null 2>&1 || true
for f in "$OUT/grub.cfg"; do [ -f "$f" ] && grep -E "menuentry|linux" "$f" | sed 's/^/   /'; done
# grub.cfg may source other files; show any live entries there too.
xorriso -osirrox on -indev "$ISO" -extract /boot/grub "$OUT/grub" >/dev/null 2>&1 || true
grep -rhE "menuentry|^\s*linux" "$OUT/grub" 2>/dev/null | sort -u | sed 's/^/   /' || true

xorriso -osirrox on -indev "$ISO" -extract /live/vmlinuz "$OUT/vmlinuz" -extract /live/initrd.img "$OUT/initrd.img" >/dev/null
chmod -R u+w "$OUT"
APPEND="boot=live components loglevel=3 hostname=imagen username=user noautologin nopersistence toram console=ttyS0,115200 systemd.journald.forward_to_console=1"

ACCEL=""
[ -w /dev/kvm ] && ACCEL="-enable-kvm -cpu host" && echo "== using KVM"
# shellcheck disable=SC2086
qemu-system-x86_64 $ACCEL -m 3072 -smp 2 \
	-cdrom "$ISO" -kernel "$OUT/vmlinuz" -initrd "$OUT/initrd.img" -append "$APPEND" \
	-vga none -device "VGA,edid=on,xres=$XRES,yres=$YRES,vgamem_mb=64" -display none \
	-device qemu-xhci -device usb-tablet \
	-serial "file:$OUT/serial.log" \
	-qmp "unix:$OUT/qmp.sock,server,nowait" \
	-monitor "unix:$OUT/monitor.sock,server,nowait" &
QEMU=$!
trap 'kill $QEMU 2>/dev/null || true' EXIT

ok=0
i=0
while [ $i -lt "$TIMEOUT" ]; do
	if grep -q "interface connected" "$OUT/serial.log" 2>/dev/null; then ok=1; break; fi
	if grep -q "restart counter is at 20" "$OUT/serial.log" 2>/dev/null; then echo "kiosk keeps failing"; break; fi
	if ! kill -0 $QEMU 2>/dev/null; then echo "QEMU exited"; break; fi
	sleep 5; i=$((i + 5))
done
[ $ok = 1 ] && sleep 15 # let the page finish drawing

# click X Y: a left click at screen pixel X,Y (1280x800) through QMP.
click() {
	ax=$(($1 * 32767 / 1279)); ay=$(($2 * 32767 / 799))
	{
		echo '{"execute":"qmp_capabilities"}'
		echo '{"execute":"input-send-event","arguments":{"events":[{"type":"abs","data":{"axis":"x","value":'$ax'}},{"type":"abs","data":{"axis":"y","value":'$ay'}}]}}'
		sleep 0.3
		echo '{"execute":"input-send-event","arguments":{"events":[{"type":"btn","data":{"button":"left","down":true}}]}}'
		sleep 0.1
		echo '{"execute":"input-send-event","arguments":{"events":[{"type":"btn","data":{"button":"left","down":false}}]}}'
		sleep 0.3
	} | socat - "UNIX-CONNECT:$OUT/qmp.sock" >/dev/null || true
}

echo "screendump $OUT/screen.ppm" | socat - "UNIX-CONNECT:$OUT/monitor.sock" >/dev/null || true
sleep 2
echo "== kiosk / interface lines from the serial log"
grep -aE "imagen|cage|firefox|interface" "$OUT/serial.log" | tail -n 40 || true

if [ $ok = 1 ] && [ "${POWEROFF:-}" = 1 ]; then
	# The system runs from RAM ("toram"): take the boot medium away first, like
	# pulling the stick out, and the shutdown must still work.
	echo "== removing the boot medium"
	echo "eject -f ide1-cd0" | socat - "UNIX-CONNECT:$OUT/monitor.sock" || true
	echo "info block" | socat - "UNIX-CONNECT:$OUT/monitor.sock" | grep -a "ide1-cd0" || true
	sleep 5
	echo "== clicking Shut down"
	click 1186 33; sleep 3
	echo "screendump $OUT/shutdown-00.ppm" | socat - "UNIX-CONNECT:$OUT/monitor.sock" >/dev/null || true
	click 831 450
	n=1
	while [ $n -le 40 ] && kill -0 $QEMU 2>/dev/null; do
		sleep 3
		echo "screendump $OUT/shutdown-$(printf %02d $n).ppm" | socat - "UNIX-CONNECT:$OUT/monitor.sock" >/dev/null 2>&1 || true
		n=$((n + 1))
	done
	kill -0 $QEMU 2>/dev/null && echo "== the PC did not power off within 120s" || echo "== the PC powered off"
	echo "== serial log from the click on"
	sed -n '/interface connected/,$p' "$OUT/serial.log" | tail -n 150
fi

if [ $ok = 1 ]; then
	echo "== PASS: the interface loaded after about ${i}s"
else
	echo "== FAIL: the interface did not load within ${TIMEOUT}s"
	tail -n 80 "$OUT/serial.log"
	exit 1
fi
