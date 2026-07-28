[server]
hostname=0.0.0.0
port=5696
# The leaf comes from step-ca (full chain: leaf + intermediate). client_ca.crt
# is the anchor set for verifying client certificates: the step-ca root and the
# intermediate that actually signs leaves, so a client presenting its leaf
# without the chain still verifies. Both are mounted read-only.
certificate_path=/etc/pykmip/certs/kmip.crt
key_path=/etc/pykmip/certs/kmip.key
ca_path=/etc/pykmip/certs/client_ca.crt
auth_suite=TLS1.2
# Client certificate authentication: vCenter and ESXi present a certificate
# this CA issued, and PyKMIP maps it to an identity by common name.
enable_tls_client_auth=True
policy_path=/etc/pykmip/policies
logging_level=INFO
database_path=/var/lib/pykmip/pykmip.db
