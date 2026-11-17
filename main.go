package main

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"time"

	"tinygo.org/x/bluetooth"
)

// On macOS, CoreBluetooth hides the MAC and exposes a per-host UUID instead.
const miclockId = "cea7f141-01a5-7e65-1e60-21b898968922"

const usage = `usage: miaoclock <command>
  read              temperature, humidity, battery, clock, unit
  history           dump stored hourly min/max records
  poll [seconds]    read-only: watch all readable characteristics, print changes (default 180s)
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
	if len(os.Args) < 2 {
		fmt.Println(usage)
		os.Exit(2)
	}
	cmd := os.Args[1]
	switch cmd {
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
		panic("clock service not found")
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
			panic("characteristic not found: " + short)
		}
		return c
	}

	switch cmd {
	case "read":
		doRead(get)
	case "history":
		doHistory(get("ccbc"))
	case "poll":
		doPoll(byID, get("ccc1"))
	case "set-time":
		setTime(get("ccb7"), tsToSet, tzHours)
		fmt.Println("after:")
		printTime(get("ccb7"))
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
	case "raw-unit":
		raw, err := hex.DecodeString(os.Args[2])
		must("parse hex", err)
		_, err = get("ccbe").Write(raw)
		must("write unit", err)
		fmt.Printf("wrote %x\n", raw)
	}
}

func mustSvc() bluetooth.UUID {
	u, err := bluetooth.ParseUUID(svcUUID)
	must("parse uuid", err)
	return u
}

func connect() bluetooth.Device {
	var found bluetooth.ScanResult
	ok := make(chan struct{})
	fmt.Println("scanning...")
	go func() {
		err := adapter.Scan(func(a *bluetooth.Adapter, d bluetooth.ScanResult) {
			if d.Address.String() == miclockId {
				found = d
				a.StopScan()
				close(ok)
			}
		})
		must("scan", err)
	}()
	select {
	case <-ok:
	case <-time.After(30 * time.Second):
		panic("device not found in 30s")
	}
	dev, err := adapter.Connect(found.Address, bluetooth.ConnectionParams{})
	must("connect", err)
	return dev
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
	fmt.Printf("wrote %02x\n", b)
}

func doRead(get func(string) bluetooth.DeviceCharacteristic) {
	printTime(get("ccb7"))
	u := readAll(get("ccbe"))
	unit := "?"
	if len(u) > 0 {
		switch u[0] {
		case unitByteF:
			unit = "Fahrenheit"
		case unitByteC:
			unit = "Celsius"
		}
		fmt.Printf("unit byte: %02x (%s)\n", u[0], unit)
	}
	if b := readAll(get("ccc4")); len(b) > 0 {
		fmt.Printf("battery: %d%%\n", b[0])
	}

	got := make(chan []byte, 1)
	err := get("ccc1").EnableNotifications(func(b []byte) {
		select {
		case got <- append([]byte(nil), b...):
		default:
		}
	})
	must("subscribe", err)
	select {
	case b := <-got:
		if len(b) >= 3 {
			t := float64(int16(binary.LittleEndian.Uint16(b))) / 100
			fmt.Printf("temperature: %.2f °C / %.1f °F\nhumidity: %d%%\n", t, t*9/5+32, b[2])
		}
	case <-time.After(15 * time.Second):
		fmt.Println("no temperature notification within 15s")
	}
}

func printTime(c bluetooth.DeviceCharacteristic) {
	b := readAll(c)
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
	fmt.Printf("wrote time %x (%s, tz %+d)\n", b, t.Format(time.RFC3339), tz)
}

// doPoll is read-only. It snapshots every readable characteristic of the clock
// service every 2s and prints only the ones that changed (the clock's own
// ticking timestamp in ccb7/ccbb is shown but flagged as expected noise).
func doPoll(byID map[string]bluetooth.DeviceCharacteristic, live bluetooth.DeviceCharacteristic) {
	secs := 180
	if len(os.Args) > 2 {
		fmt.Sscanf(os.Args[2], "%d", &secs)
	}
	short := func(id string) string { return id[4:8] } // ebe0XXXX-...
	var env struct{ t, h string }
	_ = live.EnableNotifications(func(b []byte) {
		if len(b) >= 3 {
			env.t = fmt.Sprintf("%.2fC", float64(int16(binary.LittleEndian.Uint16(b)))/100)
			env.h = fmt.Sprintf("%d%%", b[2])
		}
	})

	prev := map[string]string{}
	skip := map[string]bool{short(uuid("ccbc").String()): true} // history, notify-only
	start := time.Now()
	lastEnv := ""
	for time.Since(start) < time.Duration(secs)*time.Second {
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
		if e := env.t + " " + env.h; e != lastEnv {
			fmt.Printf("[%v] live temp/humidity: %s\n", stamp, e)
			lastEnv = e
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
	fmt.Fprintf(os.Stderr, "%d records\n", count)
}

func must(action string, err error) {
	if err != nil {
		panic("failed to " + action + ": " + err.Error())
	}
}
