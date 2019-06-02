desc "This task is called by the cron daemon to make sure monthly payroll is run"
task :run_payroll => :environment do
  puts "Sleeping for a random period of time to aviod multiple taskings..."
  sleep(rand(120))
  puts "Running Payroll..."
  Budget.all.each do |budget|

    # Can retry if fails 
    if budget.payroll_run_at and budget.payroll_run_at < Date.today.at_beginning_of_month
      budget.run_payroll
      puts "  - payroll for #{budget.name} complete."
    end
  end
  puts "done."
end
