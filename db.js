var orm = require("orm");
 
orm.connect(process.env.DATABASE_URL, function (err, db) {
  if (err) throw err;
 
  // Will be populated using the Google Identity Toolkit (oauth)
  var user = db.define("person", {
    id        : String, // Google unique ID
    nickname  : String,
    email     : String,
    data      : Object // JSON encoded 
  }, {
    methods: {
      email: function () {
        return this.email;
      }
    },
    validations: {
      email: orm.enforce.patterns.email("Invalid e-mail address")
    }
  });
 
  // add the table to the database 
  db.sync(function(err) { 
    if (err) throw err;
 
    /*
    // add a row to the person table 
    user.create({ id: 1, name: "John", surname: "Doe", age: 27 }, function(err) {
      if (err) throw err;
 
        // query the person table by surname 
        user.find({ surname: "Doe" }, function (err, people) {
              // SQL: "SELECT * FROM person WHERE surname = 'Doe'" 
              if (err) throw err;
 
              console.log("People found: %d", people.length);
              console.log("First person: %s, age %d", people[0].fullName(), people[0].age);
 
              people[0].age = 16;
              people[0].save(function (err) {
                  // err.msg = "under-age"; 
            });
        });
      
    });
    */
  });
});
