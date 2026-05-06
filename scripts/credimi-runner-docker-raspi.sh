docker rm -f credimi-runner 2>/dev/null || true

docker run --rm -it --name credimi-runner --network host --privileged \
  -v adbkeys:/root/.android \
  -e CREDIMI_INTERNAL_ADMIN_KEY=internal-admin-key \
  -e CREDIMI_URL=https://credimi.io \
  -e TEMPORAL_ADDRESS=temporal.credimi.io:7233 \
  -e CREDIMI_RUNNER_ID=/forkbomb-bv-andrea/android-cloud-credimi-1 \
  ghcr.io/forkbombeu/credimi-runner-phone:latest 127.0.0.1:5555
