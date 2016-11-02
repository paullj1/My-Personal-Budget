desc "This task is called by the Heroku scheduler add-on"
task :run_payroll => :environment do
  # Gets run every day, check for first of month
  if Time.zone.now.day == 1 # scheduler runs in UTC, this makes sure we're in the right zone
    puts "Running Payroll..."
    Budget.all.each do |budget|

      # Can retry if fails 
      if budget.payroll_run_at < Date.today.at_beginning_of_month
        budget.run_payroll
        puts "  - payroll for #{budget.name} complete."
      end

    end
    puts "done."
  end
end
