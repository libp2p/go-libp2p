# Debugging Flaky Tests

If a test is flaky in CI it's probably because there's some timing issue. The
test probably depends on some Go routine making progress in the background and
polling to see if the expected outcome is achieved.

This will pretty much always work locally because your local machine is likely
pretty capable and there isn't too many concurrent processes running. In CI, we
are susceptible to both slower hardware and noisier neighbors. However we can
mimic this environment locally with
[cgroups](https://man7.org/linux/man-pages/man7/cgroups.7.html).

# Replicating noisy neighbors

We can limit the amount of CPU time relative to real time a process gets with
cgroups. This lets us replicate the environment where many other neighboring
processes are vying for CPU time.

```bash
  # Compile some test we want to run. We do this outside the cgroup so this is
  # fast
  go test -c ./p2p/host/autorelay

  # Create the group
  sudo cgcreate -g cpu:/cpulimit

  # Limit the time to 10,000 microseconds for every 1s
  sudo cgset -r cpu.cfs_quota_us=10000 cpulimit
  sudo cgset -r cpu.cfs_period_us=1000000 cpulimit

  # Run a shell with in our limited environemnt
  sudo cgexec -g cpu:cpulimit bash

  # In the shell, run the test
  ./autorelay.test -test.v
```

# Flakiness with coverage profile

Sometimes adding the `-coverprofile=module-coverage.txt` introduces flaky
behavior since it adds another goroutine to the mix. If you're having trouble
reproducing a flaky test, try enabling this flag.

