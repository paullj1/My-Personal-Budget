desc "This task is called by the Heroku scheduler add-on"
task :run_payroll => :environment do
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
