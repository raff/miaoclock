package main

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"tinygo.org/x/bluetooth"
)

// target selects the clock to talk to: a MAC address (AA:BB:CC:DD:EE:FF) or a
// platform address (e.g. a CoreBluetooth UUID on macOS). Empty means
// auto-detect, which works only if exactly one LYWSD02 is in range.
var target = os.Getenv("MIAOCLOCK_DEVICE")

// verbose enables progress messages (on stderr) and the raw/technical output
// (unix time, raw bytes, characteristic changes). Set with -v / --verbose.
var verbose bool

// verbosef prints a progress message to stderr, only in verbose mode.
func verbosef(format string, args ...any) {
	if verbose {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	}
}

const usage = `usage: miaoclock [-d <mac|address>] [-v] <command>
  -d, --device      which clock to use (default: $MIAOCLOCK_DEVICE, else auto-detect a single LYWSD02)
  -v, --verbose     show progress messages and raw technical details
  scan [seconds] [--all]
                    list nearby LYWSD02 clocks with their MAC (--all: every BLE device)
  read              show time, temperature (C and F), humidity, unit, battery.
                    This is the default with no command.
  history           print the stored hourly min/max records as CSV
  poll [seconds]    live monitor: print time, temperature and humidity on every change
                    (default 180s). With -v, show every raw characteristic change instead
  help              show this text
  set-time now|<RFC3339> [--tz <hours>]
                    set the clock: "now" uses this computer's time and timezone;
                    otherwise e.g. 2026-11-16T18:30:00-08:00 (offset becomes the clock's tz)
                    --tz overrides the timezone byte (whole hours from UTC)
  set-unit c|f      switch display unit
  raw-unit <hex>    write a raw byte to the unit characteristic (for probing)`

// Unit byte values. Observed: device showing °F reads 0x01, so 0x01 = F here.
// (Other LYWSD02 docs claim the opposite; verify with the display.)
const (
	unitByteF = 0x01
	unitByteC = 0xFF
)

var adapter = bluetooth.DefaultAdapter

const svcUUID = "ebe0ccb0-7a0a-4b0c-8a1a-6ff2997da3a6"

func uuid(short string) bluetooth.UUID {
	u, err := bluetooth.ParseUUID("ebe0" + short + "-7a0a-4b0c-8a1a-6ff2997da3a6")
	must("parse uuid", err)
	return u
}

func main() {
	args := os.Args[1:]
parse:
	for len(args) > 0 {
		switch args[0] {
		case "-d", "--device":
			if len(args) < 2 {
				fmt.Println(usage)
				os.Exit(2)
			}
			target = args[1]
			args = args[2:]
		case "-v", "--verbose":
			verbose = true
			args = args[1:]
		default:
			break parse
		}
	}
	os.Args = append([]string{os.Args[0]}, args...)
	if len(os.Args) < 2 {
		os.Args = append(os.Args, "read") // default: show the current values
	}
	cmd := os.Args[1]
	switch cmd {
	case "-h", "--help", "help":
		fmt.Println(usage)
		return
	case "scan":
		must("enable BLE stack", adapter.Enable())
		doScan()
		return
	case "read", "history", "set-time", "set-unit", "raw-unit", "poll":
	default:
		fmt.Println(usage)
		os.Exit(2)
	}
	if (cmd == "set-unit" || cmd == "raw-unit" || cmd == "set-time") && len(os.Args) < 3 {
		fmt.Println(usage)
		os.Exit(2)
	}
	var tsToSet time.Time
	var tzHours int
	if cmd == "set-time" {
		tsToSet, tzHours = parseSetTime(os.Args[2:])
	}

	must("enable BLE stack", adapter.Enable())
	dev := connect()
	defer dev.Disconnect()

	svcs, err := dev.DiscoverServices([]bluetooth.UUID{mustSvc()})
	must("discover services", err)
	if len(svcs) == 0 {
		fatal("clock service not found on this device (is it really a LYWSD02?)")
	}
	chars, err := svcs[0].DiscoverCharacteristics(nil)
	must("discover characteristics", err)
	byID := map[string]bluetooth.DeviceCharacteristic{}
	for _, c := range chars {
		byID[c.UUID().String()] = c
	}
	get := func(short string) bluetooth.DeviceCharacteristic {
		c, ok := byID[uuid(short).String()]
		if !ok {
			fatal("characteristic not found: " + short)
		}
		return c
	}

	switch cmd {
	case "read":
		doRead(get)
	case "history":
		doHistory(get("ccbc"))
	case "poll":
		doPoll(byID, get)
	case "set-time":
		setTime(get("ccb7"), tsToSet, tzHours)
		if verbose {
			fmt.Println("after:")
			printTime(get("ccb7"))
		}
	case "set-unit":
		var b byte
		switch os.Args[2] {
		case "c", "C":
			b = unitByteC
		case "f", "F":
			b = unitByteF
		default:
			fmt.Println(usage)
			os.Exit(2)
		}
		writeByte(get("ccbe"), b)
		fmt.Println("Display unit set to", unitName(b))
	case "raw-unit":
		raw, err := hex.DecodeString(os.Args[2])
		must("parse hex", err)
		_, err = get("ccbe").Write(raw)
		must("write unit", err)
		fmt.Printf("wrote %x\n", raw)
	}
}

// doScan lists nearby BLE devices with what they advertise (read-only).
func doScan() {
	secs := 10
	all := false
	for _, a := range os.Args[2:] {
		if a == "--all" {
			all = true
		} else if _, err := fmt.Sscanf(a, "%d", &secs); err != nil {
			fmt.Println(usage)
			os.Exit(2)
		}
	}
	seen := map[string]bool{}
	clocks := 0
	go func() {
		err := adapter.Scan(func(a *bluetooth.Adapter, d bluetooth.ScanResult) {
			addr := d.Address.String()
			if seen[addr] {
				return
			}
			seen[addr] = true
			mac, isClock := lywsdMAC(d)
			if !isClock && !all {
				return
			}
			if isClock {
				clocks++
				fmt.Printf("LYWSD02 %s (address %s, rssi %d)\n", mac, addr, d.RSSI)
				return
			}
			var svcs []string
			for _, u := range d.ServiceUUIDs() {
				svcs = append(svcs, u.String())
			}
			var sd []string
			for _, s := range d.ServiceData() {
				sd = append(sd, fmt.Sprintf("%s:%x", s.UUID.String(), s.Data))
			}
			fmt.Printf("other   %s rssi=%d name=%q services=%v svcdata=%v\n", addr, d.RSSI, d.LocalName(), svcs, sd)
		})
		must("scan", err)
	}()
	time.Sleep(time.Duration(secs) * time.Second)
	adapter.StopScan()
	if clocks == 0 {
		fmt.Fprintln(os.Stderr, "no LYWSD02 clocks found")
	}
}

func mustSvc() bluetooth.UUID {
	u, err := bluetooth.ParseUUID(svcUUID)
	must("parse uuid", err)
	return u
}

// lywsdMAC reports whether the advertisement is from a LYWSD02, and its MAC.
// The clock advertises Xiaomi MiBeacon service data (UUID fe95):
//
//	[0:2] frame control (bit 4 = MAC present), [2:4] product id (0x045b = LYWSD02),
//	[4] frame counter, [5:11] MAC address in reverse byte order.
func lywsdMAC(d bluetooth.ScanResult) (string, bool) {
	for _, s := range d.ServiceData() {
		if s.UUID.String() != "0000fe95-0000-1000-8000-00805f9b34fb" || len(s.Data) < 11 {
			continue
		}
		b := s.Data
		if binary.LittleEndian.Uint16(b[2:]) != 0x045b || b[0]&0x10 == 0 {
			continue
		}
		return fmt.Sprintf("%02X:%02X:%02X:%02X:%02X:%02X", b[10], b[9], b[8], b[7], b[6], b[5]), true
	}
	return "", false
}

// normID strips separators and case so MACs (AA:BB:.. or AA-BB-..) and UUIDs
// (which contain dashes) compare equal regardless of how they were typed.
func normID(s string) string {
	return strings.ToUpper(strings.NewReplacer(":", "", "-", "", " ", "").Replace(s))
}

// matches reports whether a scan result is the selected clock.
func matches(d bluetooth.ScanResult) bool {
	mac, isClock := lywsdMAC(d)
	if target == "" {
		return isClock
	}
	t := normID(target)
	return normID(d.Address.String()) == t || (isClock && normID(mac) == t)
}

func connect() bluetooth.Device {
	var mu sync.Mutex
	cands := map[string]bluetooth.ScanResult{} // by address
	verbosef("scanning...")
	go func() {
		err := adapter.Scan(func(a *bluetooth.Adapter, d bluetooth.ScanResult) {
			if !matches(d) {
				return
			}
			mu.Lock()
			cands[d.Address.String()] = d
			mu.Unlock()
		})
		must("scan", err)
	}()

	// With an explicit target, take the first match. Otherwise listen for a few
	// seconds so that more than one clock in range is noticed.
	deadline := time.Now().Add(30 * time.Second)
	settle := time.Time{}
	for time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		mu.Lock()
		n := len(cands)
		mu.Unlock()
		if n == 0 {
			continue
		}
		if target != "" {
			break
		}
		if settle.IsZero() {
			settle = time.Now().Add(5 * time.Second)
		} else if time.Now().After(settle) {
			break
		}
	}
	adapter.StopScan()

	mu.Lock()
	defer mu.Unlock()
	switch len(cands) {
	case 0:
		if target == "" {
			fatal("no LYWSD02 found in 30s (try 'miaoclock scan')")
		}
		fatal("device " + target + " not found in 30s (try 'miaoclock scan')")
	case 1:
	default:
		fmt.Fprintln(os.Stderr, "miaoclock: several LYWSD02 clocks in range; pick one with -d or MIAOCLOCK_DEVICE:")
		for _, d := range cands {
			mac, _ := lywsdMAC(d)
			fmt.Fprintf(os.Stderr, "  %s (address %s, rssi %d)\n", mac, d.Address.String(), d.RSSI)
		}
		os.Exit(2)
	}
	for _, found := range cands {
		if mac, ok := lywsdMAC(found); ok {
			verbosef("using %s (address %s)", mac, found.Address.String())
		}
		dev, err := adapter.Connect(found.Address, bluetooth.ConnectionParams{})
		must("connect", err)
		return dev
	}
	fatal("no device selected")
	return bluetooth.Device{}
}

func readAll(c bluetooth.DeviceCharacteristic) []byte {
	buf := make([]byte, 255)
	n, err := c.Read(buf)
	must("read", err)
	return buf[:n]
}

func writeByte(c bluetooth.DeviceCharacteristic, b byte) {
	_, err := c.Write([]byte{b})
	must("write", err)
	verbosef("wrote %02x", b)
}

// unitName names a unit byte; isFahrenheit reports whether it selects °F.
func unitName(b byte) string {
	switch b {
	case unitByteF:
		return "Fahrenheit"
	case unitByteC:
		return "Celsius"
	}
	return fmt.Sprintf("unknown (0x%02x)", b)
}

func isFahrenheit(b byte) bool { return b == unitByteF }

// fmtTemp formats a temperature given in °C in the requested unit.
func fmtTemp(c float64, fahrenheit bool) string {
	if fahrenheit {
		return fmt.Sprintf("%.1f °F", c*9/5+32)
	}
	return fmt.Sprintf("%.1f °C", c)
}

// fmtClock renders a clock value the way the display does: the UTC timestamp
// shifted by the timezone byte.
func fmtClock(ts int64, tz int8) string {
	t := time.Unix(ts, 0).UTC().Add(time.Duration(tz) * time.Hour)
	return fmt.Sprintf("%s (UTC%+03d:00)", t.Format("2006-01-02 15:04:05"), tz)
}

// waitReading waits for one live temperature (°C) and humidity (%) notification.
func waitReading(c bluetooth.DeviceCharacteristic) (float64, int, bool) {
	got := make(chan []byte, 1)
	err := c.EnableNotifications(func(b []byte) {
		select {
		case got <- append([]byte(nil), b...):
		default:
		}
	})
	must("subscribe", err)
	select {
	case b := <-got:
		if len(b) >= 3 {
			return float64(int16(binary.LittleEndian.Uint16(b))) / 100, int(b[2]), true
		}
	case <-time.After(15 * time.Second):
	}
	return 0, 0, false
}

func doRead(get func(string) bluetooth.DeviceCharacteristic) {
	tb := readAll(get("ccb7"))
	ub := readAll(get("ccbe"))
	bb := readAll(get("ccc4"))
	temp, hum, ok := waitReading(get("ccc1"))

	if verbose {
		printClockRaw(tb)
		if len(ub) > 0 {
			fmt.Printf("unit byte: %02x (%s)\n", ub[0], unitName(ub[0]))
		}
		if len(bb) > 0 {
			fmt.Printf("battery: %d%%\n", bb[0])
		}
		if ok {
			fmt.Printf("temperature: %.2f °C / %.1f °F\nhumidity: %d%%\n", temp, temp*9/5+32, hum)
		}
	} else {
		if len(tb) >= 5 {
			fmt.Printf("Time:         %s\n", fmtClock(int64(binary.LittleEndian.Uint32(tb)), int8(tb[4])))
		}
		if ok {
			fmt.Printf("Temperature:  %.1f °C / %.1f °F\n", temp, temp*9/5+32)
			fmt.Printf("Humidity:     %d%%\n", hum)
		}
		if len(ub) > 0 {
			fmt.Printf("Unit:         %s\n", unitName(ub[0]))
		}
		if len(bb) > 0 {
			fmt.Printf("Battery:      %d%%\n", bb[0])
		}
	}
	if !ok {
		fatal("no temperature reading received within 15s")
	}
}

func printTime(c bluetooth.DeviceCharacteristic) { printClockRaw(readAll(c)) }

func printClockRaw(b []byte) {
	if len(b) < 5 {
		fmt.Printf("clock: unexpected %x\n", b)
		return
	}
	ts := int64(binary.LittleEndian.Uint32(b))
	tz := int8(b[4])
	fmt.Printf("clock: unix %d = %s (tz offset byte %+d)\n", ts, time.Unix(ts, 0).Format(time.RFC3339), tz)
}

// parseSetTime handles: now|<RFC3339> [--tz <hours>]
func parseSetTime(args []string) (time.Time, int) {
	var t time.Time
	if args[0] == "now" {
		t = time.Now()
	} else {
		var err error
		t, err = time.Parse(time.RFC3339, args[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, "bad time (want RFC3339, e.g. 2026-11-16T18:30:00-08:00):", err)
			os.Exit(2)
		}
	}
	_, off := t.Zone()
	tz := off / 3600
	if len(args) > 1 {
		if len(args) != 3 || args[1] != "--tz" {
			fmt.Println(usage)
			os.Exit(2)
		}
		if _, err := fmt.Sscanf(args[2], "%d", &tz); err != nil || tz < -12 || tz > 14 {
			fmt.Fprintln(os.Stderr, "bad --tz: want whole hours from UTC, -12..14")
			os.Exit(2)
		}
	}
	return t, tz
}

func setTime(c bluetooth.DeviceCharacteristic, t time.Time, tz int) {
	b := make([]byte, 5)
	binary.LittleEndian.PutUint32(b, uint32(t.Unix()))
	b[4] = byte(int8(tz))
	_, err := c.Write(b)
	must("write time", err)
	if verbose {
		fmt.Printf("wrote time %x (%s, tz %+d)\n", b, t.Format(time.RFC3339), tz)
	} else {
		fmt.Println("Clock set to", fmtClock(t.Unix(), int8(tz)))
	}
}

// doPoll is read-only. By default it prints each new live temperature/humidity
// reading. With -v it instead snapshots every readable characteristic every 2s
// and prints the ones that changed (the clock's own ticking timestamp in
// ccb7/ccbb is expected noise).
func doPoll(byID map[string]bluetooth.DeviceCharacteristic, get func(string) bluetooth.DeviceCharacteristic) {
	secs := 180
	if len(os.Args) > 2 {
		if _, err := fmt.Sscanf(os.Args[2], "%d", &secs); err != nil {
			fmt.Println(usage)
			os.Exit(2)
		}
	}
	var mu sync.Mutex
	var t float64
	var h int
	have := false
	must("subscribe", get("ccc1").EnableNotifications(func(b []byte) {
		if len(b) >= 3 {
			mu.Lock()
			t, h, have = float64(int16(binary.LittleEndian.Uint16(b)))/100, int(b[2]), true
			mu.Unlock()
		}
	}))
	latest := func() (float64, int, bool) {
		mu.Lock()
		defer mu.Unlock()
		return t, h, have
	}
	deadline := time.Now().Add(time.Duration(secs) * time.Second)

	if !verbose {
		fmt.Println("Time      Temperature          Humidity")
		last := ""
		for time.Now().Before(deadline) {
			if ct, ch, ok := latest(); ok {
				// Compare what is printed, so sub-display-precision jitter is ignored.
				if line := fmt.Sprintf("%.1f °C / %.1f °F   %d%%", ct, ct*9/5+32, ch); line != last {
					last = line
					fmt.Printf("%s  %s\n", time.Now().Format("15:04:05"), line)
				}
			}
			time.Sleep(time.Second)
		}
		return
	}

	short := func(id string) string { return id[4:8] } // ebe0XXXX-...
	prev := map[string]string{}
	skip := map[string]bool{short(uuid("ccbc").String()): true} // history, notify-only
	start := time.Now()
	lastEnv := ""
	for time.Now().Before(deadline) {
		stamp := time.Since(start).Round(time.Second)
		for id, c := range byID {
			s := short(id)
			if skip[s] || s == "ccc1" {
				continue
			}
			buf := make([]byte, 255)
			n, err := c.Read(buf)
			if err != nil || n == 0 {
				continue
			}
			cur := hex.EncodeToString(buf[:n])
			if old, ok := prev[s]; !ok {
				fmt.Printf("[%v] %s initial %s\n", stamp, s, cur)
			} else if old != cur {
				fmt.Printf("[%v] %s %s -> %s\n", stamp, s, old, cur)
			}
			prev[s] = cur
		}
		if ct, ch, ok := latest(); ok {
			if e := fmt.Sprintf("%.2fC %d%%", ct, ch); e != lastEnv {
				fmt.Printf("[%v] live temp/humidity: %s\n", stamp, e)
				lastEnv = e
			}
		}
		time.Sleep(2 * time.Second)
	}
}

func doHistory(c bluetooth.DeviceCharacteristic) {
	last := time.Now()
	count := 0
	err := c.EnableNotifications(func(b []byte) {
		last = time.Now()
		if len(b) < 14 {
			return
		}
		idx := binary.LittleEndian.Uint32(b)
		ts := time.Unix(int64(binary.LittleEndian.Uint32(b[4:])), 0)
		tmax := float64(int16(binary.LittleEndian.Uint16(b[8:]))) / 100
		hmax := b[10]
		tmin := float64(int16(binary.LittleEndian.Uint16(b[11:]))) / 100
		hmin := b[13]
		count++
		fmt.Printf("%d,%s,%.2f,%d,%.2f,%d\n", idx, ts.Format(time.RFC3339), tmax, hmax, tmin, hmin)
	})
	must("subscribe", err)
	fmt.Println("idx,time,tmax_c,hmax,tmin_c,hmin")
	for time.Since(last) < 5*time.Second {
		time.Sleep(500 * time.Millisecond)
	}
	verbosef("%d records", count)
}

// fatal logs an error and exits with status 1.
func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "miaoclock:", msg)
	os.Exit(1)
}

func must(action string, err error) {
	if err != nil {
		fatal("failed to " + action + ": " + err.Error())
	}
}
