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

The device is found by address in `main.go` (`miclockId`). On macOS, CoreBluetooth hides the real MAC address and
exposes a per-computer UUID instead, so **you must change this constant to your own device's address**. On
Linux/Windows it should be the MAC address. To find it, scan and look for a device named `LYWSD02`.

## Commands

| Command | What it does |
|---|---|
| `read` | Clock, unit, battery, plus one live temperature/humidity reading |
| `history` | Dumps stored hourly records as CSV (`idx,time,tmax_c,hmax,tmin_c,hmin`) on stdout; count on stderr |
| `poll [seconds]` | Read-only: snapshots every readable characteristic every 2 s and prints what changes (default 180 s) |
| `set-time now` | Sets the clock from this computer's time and timezone offset |
| `set-time <RFC3339>` | Sets a specific time, e.g. `2026-11-16T18:30:00-08:00`; the string's offset becomes the timezone byte |
| `set-time ... --tz <h>` | Overrides the timezone byte (whole hours from UTC, -12..14) |
| `set-unit c\|f` | Switches the display between Celsius and Fahrenheit |
| `raw-unit <hex>` | Writes raw bytes to the unit characteristic (for probing) |

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
