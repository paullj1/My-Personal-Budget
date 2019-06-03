# README
This project is in the process of being migrated to run as a Docker service or
part of a docker stack on a docker swarm.  The following are dependencies to
make that work:

* Define the password that the container will use to access the database as a
  secret labelled 'budgetpass'.
* MPB is configured to use a postgresql database with a dns record labelled
  'db'.  If you'd like to change this, modify the 'Production' directive in
  'config/database.yml'.
* You'll also need to define the 'sendgrid_user', and 'sendgrid_pass' secrets
* Finally, to protect against CSRF, set the 'secretkeybase' secret to be long
  and random.
