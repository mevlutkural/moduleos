UPDATE applications
SET expose = 0
WHERE expose = 1 AND ingress_container_port = 0;
