---
name: esp-idf-m5stack
description: Build and verify ESP-IDF v6.0.1 firmware for M5Stack (Atom / AtomS3 / CoreS3) using idf.py and QEMU.
---

# ESP-IDF & M5Stack (Atom / AtomS3) Workflow

`idf.py` is available directly in this environment.

## Target Selection
Set the target chip before building if `sdkconfig` is not yet configured:
- **M5Stack Atom Lite / Atom Matrix / Atom Echo (ESP32)**:
  `idf.py set-target esp32`
- **M5Stack AtomS3 / AtomS3 Lite / AtomS3R / CoreS3 (ESP32-S3)**:
  `idf.py set-target esp32s3`

## Build & Verify
1. Build firmware:
   `idf.py build`
2. Run in QEMU (automatically bounded to 15s and captures serial output):
   `idf.py qemu`
   - Check serial output for boot banner, `app_main`, and absence of `Guru Meditation Error` / `abort()` / `assert failed`.
3. Clean build artifacts if needed:
   `idf.py fullclean`

## Notes
- Physical flashing (`idf.py flash`) is done by the user on the Windows host after `/workspace` passes `idf.py build` and `idf.py qemu`.
