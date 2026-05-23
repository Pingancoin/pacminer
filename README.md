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

Defaults:

- Stratum: `stratum.pingancoin.org:3333`
- Password: `x`
- Threads: half of the local CPU cores
- Suggested share difficulty: `5000000`

The username before the first dot is treated as the payout address by
`pacpool`. For example, `Pabc...xyz.rig01` pays `Pabc...xyz`.

This first release is a CPU test miner. It is meant to verify pool connectivity,
share acceptance, worker accounting, and solved-block submission. It is not an
ASIC or GPU optimized miner.
