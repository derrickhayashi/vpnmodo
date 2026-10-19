FROM vpnmodo-l2tp:base-acceptance
COPY entrypoint /usr/local/bin/lab-client-entrypoint
RUN chmod 0755 /usr/local/bin/lab-client-entrypoint
ENTRYPOINT ["/usr/local/bin/lab-client-entrypoint"]
