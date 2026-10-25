FROM public.ecr.aws/docker/library/alpine:latest
COPY --chmod=755 tailscale_1.102.4_amd64/tailscale /usr/local/bin/tailscale
COPY --chmod=755 tailscale_1.102.4_amd64/tailscaled /usr/local/bin/tailscaled
ENTRYPOINT ["tailscaled"]
CMD ["--tun=userspace-networking", "--statedir=/var/lib/tailscale", "--socket=/tmp/tailscaled.sock"]
