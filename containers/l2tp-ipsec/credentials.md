# L2TP/IPsec credential files

Create three files in the consumer runtime's `secrets/` directory:

- `username` — the PPP account name
- `password` — the PPP account password
- `psk` — the IPsec pre-shared key

Do not add quotation marks. A trailing newline is accepted. Restrict the
directory to mode 700 and files to 600. Keep runtime secrets outside this
source repository and out of consumer source control.
