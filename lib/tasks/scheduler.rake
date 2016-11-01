desc "This task is called by the Heroku scheduler add-on"
task :run_payroll => :environment do
  # Gets run every day, check for first of month
  if DateTime.now.day == 1 # scheduler runs in UTC, this makes sure we're in the right zone
    puts "Running Payroll..."
    Budget.all.each do |budget|

      # Can retry if fails 
      if budget.payroll_run_at < Date.today.at_beginning_of_month
        PayrollJob.perform_later budget.id
        puts "  - payroll for #{budget.name} scheduled."
      end

    end
    puts "done."
  end
end
