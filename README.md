# pacminer

Pingancoin Windows CPU test miner.

This is a foreground console miner for testing the official PAC pool Stratum
path. It does not install a service, does not auto-start, and stops when the
console window is closed.

## Windows usage

Open PowerShell in the folder that contains `pacminer.exe`:

```powershell
.\pacminer.exe --user PYourWalletAddress.rig01
```

Useful options:

```powershell
.\pacminer.exe --url stratum.pingancoin.org:3333 --user PYourWalletAddress.rig01 --threads 4
.\pacminer.exe --benchmark --seconds 10 --threads 4
```

## Windows GPU mining

The Windows build includes an experimental OpenCL backend for Intel Arc,
AMD, and NVIDIA drivers. For Intel Arc B580, install the current Intel
Graphics driver first, then list devices:

```powershell
.\pacminer.exe --list-devices
```

Run a GPU benchmark:

```powershell
.\pacminer.exe --backend opencl --device 0 --benchmark --seconds 10
```

Mine with the GPU:

```powershell
.\pacminer.exe --backend opencl --device 0 --user PYourWalletAddress.rig01
```

If multiple OpenCL devices are shown, change `--device 0` to the Intel Arc B580
device index.

Defaults:

- Stratum: `stratum.pingancoin.org:3333`
- Password: `x`
- Threads: half of the local CPU cores
- Suggested share difficulty: `5000000`

The username before the first dot is treated as the payout address by
`pacpool`. For example, `Pabc...xyz.rig01` pays `Pabc...xyz`.

This release includes the PAC 180-byte header path used by the current pool and
an experimental OpenCL backend for GPU mining.
