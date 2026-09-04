
import os
import socket
import subprocess
import sys
import traceback

def _blocked(*args, **kwargs):
    raise RuntimeError("Network and subprocess access are disabled in this code interpreter runtime")

socket.socket = _blocked
socket.create_connection = _blocked
subprocess.Popen = _blocked
subprocess.run = _blocked
subprocess.call = _blocked
subprocess.check_call = _blocked
subprocess.check_output = _blocked
os.system = _blocked
os.popen = _blocked

code_path = sys.argv[1]
globals_dict = {"__name__": "__main__", "__file__": code_path}

try:
    with open(code_path, "r", encoding="utf-8") as handle:
        source = handle.read()
    exec(compile(source, code_path, "exec"), globals_dict)
except SystemExit:
    raise
except BaseException:
    traceback.print_exc()
    sys.exit(1)
