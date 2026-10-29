# Regenerates qr_vectors.json with an independent encoder (python-qrcode 8.2):
#   python3 -m venv /tmp/qrref && /tmp/qrref/bin/pip install qrcode==8.2
#   /tmp/qrref/bin/python internal/qr/testdata/gen_vectors.py > internal/qr/testdata/qr_vectors.json
# The mask is fixed per case: encoders score masks differently, and any mask decodes.
import json, qrcode
from qrcode.util import QRData, MODE_8BIT_BYTE

fp = "9f3c" + "a1" * 30
inputs = [
    "roger://join?kind=tower-client&at=192.168.1.20%3A8787&fp=" + fp + "&code=0123456789ABCDEFGHJKMNPQRS",
    "roger://join?kind=edge&at=192.168.1.69%3A39009&fp=" + fp + "&code=ABCDE12345",
    "roger://join?kind=tower-station&at=10.0.0.5%3A8787&at=%5Bfd00%3A%3A5%5D%3A8787&fp=" + fp + "&code=0123456789ABCDEFGHJKMNPQRS",
    "hello",
    "A" * 300,
]
EC = {"L": qrcode.constants.ERROR_CORRECT_L, "M": qrcode.constants.ERROR_CORRECT_M,
      "Q": qrcode.constants.ERROR_CORRECT_Q, "H": qrcode.constants.ERROR_CORRECT_H}


def make(s, err, mask):
    q = qrcode.QRCode(version=None, error_correction=EC[err], border=0, mask_pattern=mask)
    q.add_data(QRData(s.encode(), mode=MODE_8BIT_BYTE))
    q.make(fit=True)
    return q.version, ["".join("1" if b else "0" for b in row) for row in q.get_matrix()]


cases, i = [], 0
for s in inputs:
    for err in "LMQH":
        v, m = make(s, err, i % 8)
        cases.append({"data": s, "error": err, "mask": i % 8, "version": v, "matrix": m})
        i += 1
for s in inputs[:2]:
    for k in range(8):
        v, m = make(s, "M", k)
        cases.append({"data": s, "error": "M", "mask": k, "version": v, "matrix": m})
print(json.dumps({"generator": "python-qrcode 8.2 (QRCode border=0, QRData MODE_8BIT_BYTE, mask_pattern fixed)",
                  "cases": cases}, indent=1))
