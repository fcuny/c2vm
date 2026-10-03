# Packages kernels built by build.sh as an image, one per platform:
# /boot/kernel and the config it was built with in /boot/config.
FROM scratch
ARG TARGETARCH
COPY out/${TARGETARCH}/ /boot/
