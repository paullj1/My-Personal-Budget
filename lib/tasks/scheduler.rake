desc "This task is called by the Heroku scheduler add-on"
task :run_payroll => :environment do
  puts "Running Payroll..."
  Budget.all.each do |budget|
    PayrollJob.perform_later budget.id
  end
  puts "done."
end
