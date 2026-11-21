import os
import sys
import time

# Record this debuggee's pid for the machine smoke test, then block.
with open(sys.argv[1] + ".tmp", "w") as f:
    f.write(str(os.getpid()))
os.replace(sys.argv[1] + ".tmp", sys.argv[1])
while True:
    time.sleep(1)
