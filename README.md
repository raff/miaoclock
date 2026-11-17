# miaoclock (^_^)

A small Go CLI (using [tinygo.org/x/bluetooth](https://github.com/tinygo-org/bluetooth)) to read from and configure a
Xiaomi / Miaomiaoce **LYWSD02** BLE clock with temperature and humidity sensor. `LYWSD02` is also the device name
it reports over BLE. The same hardware is sold under other names, for example by
[Adafruit](https://www.adafruit.com/product/5023).

Device details as reported over BLE: manufacturer `miaomiaoce.com`, model `LYWSD02`, firmware `1.1.2_0097`,
hardware `F2_LN`.

## Build and run

```
go build -o miaoclock .
./miaoclock read
```

macOS will ask for Bluetooth permission for your terminal the first time.

## Choosing the clock

By default `miaoclock` scans for about 5 seconds and uses the LYWSD02 it finds. If it finds more than one, it lists
them and exits so you can choose. To pick one explicitly, give its MAC address:

```
./miaoclock scan                       # lists nearby LYWSD02 clocks and their MACs
./miaoclock -d AA:BB:CC:DD:EE:FF read  # or: export MIAOCLOCK_DEVICE=AA:BB:CC:DD:EE:FF
```

How the clock is recognized: it advertises no name, but it broadcasts Xiaomi MiBeacon service data (UUID `fe95`)
containing product ID `0x045B` (LYWSD02) and its real MAC address (in reverse byte order). The MAC is therefore
available even on macOS, where CoreBluetooth otherwise hides it and exposes only a per-computer UUID. `-d` also
accepts that platform address (the UUID on macOS) if you prefer.

## Commands

| Command | What it does |
|---|---|
| `scan [seconds] [--all]` | Lists nearby LYWSD02 clocks with their MAC (default 10 s). `--all` also shows every other BLE device |
| *(none)* | **Default with no command:** one compact line with the time, temperature (°C and °F), humidity and battery, in the same layout as `poll` |
| `read` | The same values as a multi-line list, plus the display unit |
| `history` | Prints the stored hourly records as CSV (`idx,time,tmax_c,hmax,tmin_c,hmin`, temperatures in °C) on stdout; takes about a minute |
| `poll [seconds]` | Live monitor: prints the time, temperature (°C and °F) and humidity whenever they change (default 180 s). With `-v`, snapshots every readable characteristic every 2 s and prints the raw changes instead |
| `set-time now` | Sets the clock from this computer's time and timezone offset |
| `set-time <RFC3339>` | Sets a specific time, e.g. `2026-11-16T18:30:00-08:00`; the string's offset becomes the timezone byte |
| `set-time ... --tz <h>` | Overrides the timezone byte (whole hours from UTC, -12..14) |
| `set-unit c\|f` | Switches the display between Celsius and Fahrenheit |
| `raw-unit <hex>` | Writes raw bytes to the unit characteristic (for probing) |

### Output and `-v`

By default the output is meant for people, and only data goes to stdout, so redirecting (`miaoclock history > h.csv`)
gives a clean file. Errors go to stderr and exit with status 1.

```
$ ./miaoclock
Time                 Temperature        Humidity  Battery
2026-11-16 17:55:40  22.6 °C / 72.7 °F  60%       24%

$ ./miaoclock read
Time:         2026-11-16 17:55:40 (UTC-08:00)
Temperature:  22.6 °C / 72.7 °F
Humidity:     60%
Unit:         Fahrenheit
Battery:      24%
```The time is shown the way the clock's display shows it (UTC timestamp plus the timezone byte).

`-v` / `--verbose` (before the command, e.g. `./miaoclock -v read`) adds progress messages on stderr (`scanning...`,
`using <MAC>`) and switches to the raw technical view: the unix time and timezone byte, the unit byte, both
temperature units, raw bytes written by `set-time`, and the raw characteristic changes in `poll`.

## Charting the history

`plot.sh` fetches the stored history from the clock, plots it with [gnuplot](http://www.gnuplot.info/)
(`brew install gnuplot`) using `plot.gp`, and opens the PNG: hourly min/max temperature on top, humidity below.

```
./plot.sh                          # fetch from the clock (about a minute), plot, open
./plot.sh history.csv              # plot an existing CSV, without contacting the clock
./plot.sh -n -o chart.png          # don't open the image; choose the output file
```

To keep the data for later: `./miaoclock history > history.csv`, then `./plot.sh history.csv`. Spreadsheets also
work, but they usually don't recognize the `T` and timezone offset in the `time` column as a date.

## BLE protocol

Everything useful is in service `ebe0ccb0-7a0a-4b0c-8a1a-6ff2997da3a6`. Below, `ccXX` stands for
`ebe0ccXX-7a0a-4b0c-8a1a-6ff2997da3a6`. All multi-byte values are little-endian.

### Verified on this device

| Characteristic | Access | Format | Notes |
|---|---|---|---|
| `ccc1` | notify | int16 temperature in 0.01 °C, uint8 humidity % | Arrives every few seconds while connected. `61093a` = 24.01 °C, 58% |
| `ccb7` | read/write | uint32 Unix time (UTC), int8 timezone offset in hours | Display shows **UTC timestamp + offset byte** (tested: 02:31 UTC with byte -5 displayed 21:31). The device keeps counting after a write |
| `ccbe` | read/write | 1 byte: `0x01` = Fahrenheit, `0xFF` = Celsius | Verified against the display. This is the opposite of what some LYWSD02 docs claim |
| `ccc4` | read | uint8 battery % | |
| `ccbc` | notify | uint32 index, uint32 Unix time, int16 max temp, uint8 max humidity, int16 min temp, uint8 min humidity | Dumps all stored records on subscribe (about 1,800, one per hour, 3600 s apart). Temperatures in 0.01 °C |

### Unverified or unknown

| Characteristic | Observed value | Notes |
|---|---|---|
| `ccb9` | `750f0000 760f0000` | Two uint32s. Unknown |
| `ccba` | `4f0d0000` | uint32. Unknown |
| `ccbb` | `00000000 b03b226a 910a 433a 0943 0000` | Contains a timestamp and values that look like temperature/humidity. Unknown |
| `ccc2` | `9b` | Unknown |
| `ccc3` | `00ff00ff00ff` | Unknown |
| `ccd2` | `0a 36323437373239393836` | Looks like a length byte plus the ASCII string `6247729986` |
| `ccd3` | `00` | Unknown |
| `ccc8`, `ccd4` | not readable | Unknown; possibly write-only |

None of the unknown characteristics changed during the polling experiments below, apart from the clock itself.

Other services present: Device Information (`180a`), Xiaomi auth/pairing (`fe95`, `fef5`), and vendor services
`2b43ccc0-...` and `fafafa00-...`. The auth services are not used here and are best left alone.

## Behavior notes

- **Timezone:** The byte is a fixed whole-hour offset and the clock does not adjust for daylight saving. Run
  `./miaoclock set-time now` after a DST change. Half-hour zones (e.g. India, +5:30) cannot be represented.
- **Display lag:** The e-ink display shows HH:MM and only redraws about once a minute. After a write it can take
  a few minutes to show the new time.
- **Update rate:** The sensor reports every few seconds over BLE; the history is one record per hour. No setting
  for either has been found.

## The face icon

The face on the bottom line (`( ^_^ )`, `( -^- )`, ...) is a comfort indicator. It is computed by the firmware from
the live temperature and humidity. It is not stored in any readable characteristic, so it cannot be set directly.

Observations (the face was read off the display by eye):

| Temp | Humidity | Face |
|---|---|---|
| ~24 °C | ~60% | `( ^_^ )` |
| ~19 °C | not recorded | `( ^_^ )` |
| 19.7 °C | 68% | `( ^_^ )` |
| 23.9 °C | 92% (breath on the sensor) | `( -^- )` |
| 16 °C | 35% (just out of the fridge) | `( -^- )` |

So the upper humidity limit is somewhere between 68% and 92%. The face is also not "happy" at 16 °C / 35%, so the
lower limit is either a temperature between 16 and 19 °C or a humidity above 35%. These experiments don't tell which.
Humidity readings right after taking the clock out of a fridge are unreliable (condensation).

If the comfort limits are stored on the device, the only candidates are `ccb9`, `ccba` and `ccbb`, and none of their
values look like a comfort range. They may be hard-coded in the firmware. No writes to those characteristics were
attempted, since changing undocumented values could affect calibration or history logging.

## Safety

Only `ccb7` (time) and `ccbe` (unit) have been written to, and both are easy to restore. Writing to unknown
characteristics could change calibration or break history logging, so note the original value first.

## License

MIT, see [LICENSE](LICENSE).
