FROM golang:1.16.4
COPY lfswatchdog /lfswatchdog
CMD ["/lfswatchdog"]
