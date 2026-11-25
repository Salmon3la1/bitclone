#!/bin/sh
# Boots the ISO in QEMU and waits until the kiosk browser has loaded the
# IMAGen interface. Saves the serial log and a screenshot next to the ISO.
#
#   scripts/boot-test.sh dist/imagen-amd64.iso
#
# Needs: qemu-system-x86, xorriso, socat. Uses KVM when /dev/kvm exists.
# RES=2560x1600 makes the virtual screen report that resolution as its
# preferred mode (like a laptop panel); the default is 1280x800.
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
APPEND="boot=live components loglevel=3 hostname=imagen username=user noautologin nopersistence console=ttyS0,115200 systemd.journald.forward_to_console=1"

ACCEL=""
[ -w /dev/kvm ] && ACCEL="-enable-kvm -cpu host" && echo "== using KVM"
# shellcheck disable=SC2086
qemu-system-x86_64 $ACCEL -m 3072 -smp 2 \
	-cdrom "$ISO" -kernel "$OUT/vmlinuz" -initrd "$OUT/initrd.img" -append "$APPEND" \
	-vga none -device "VGA,edid=on,xres=$XRES,yres=$YRES,vgamem_mb=64" -display none \
	-serial "file:$OUT/serial.log" \
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

echo "screendump $OUT/screen.ppm" | socat - "UNIX-CONNECT:$OUT/monitor.sock" >/dev/null || true
sleep 2
echo "== kiosk / interface lines from the serial log"
grep -aE "imagen|cage|firefox|interface" "$OUT/serial.log" | tail -n 40 || true

if [ $ok = 1 ]; then
	echo "== PASS: the interface loaded after about ${i}s"
else
	echo "== FAIL: the interface did not load within ${TIMEOUT}s"
	tail -n 80 "$OUT/serial.log"
	exit 1
fi
