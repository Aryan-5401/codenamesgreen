import './main.css';
import { Elm } from './Main.elm';

// Metadata about the user is persisted in local storage.
// If there is no user in local storage, we generate one
// with a random player ID and random guest name.
var encodedUser = localStorage.getItem('user');

var parsedUser = encodedUser ? JSON.parse(encodedUser) : null;

if (parsedUser == null || !parsedUser.player_id || !parsedUser.name) {
    // generate a new user with a random identifier and save it.

    var entropy = new Uint32Array(4); // 128 bits
    window.crypto.getRandomValues(entropy);
    var playerID = entropy.join("-");

    var guestNumber = Math.floor(Math.random() * 4095);

    parsedUser = {
      player_id: playerID,
      name: 'Guest '+ guestNumber.toString(16).toUpperCase(),
    };

    const form = document.getElementById('form');
    form.addEventListener('submit', logSubmit);

} else startElmApp();

function logSubmit(e) {
  e.preventDefault();

  var age = document.getElementById("age-field").value
  var gender = document.getElementById("gender-field").value
  var country = document.getElementById("country-field").value
  parsedUser["age"] = age;
  parsedUser["gender"] = gender;
  parsedUser["country"] = country;
  encodedUser = JSON.stringify(parsedUser)
  localStorage.setItem('user', encodedUser);

  startElmApp();
}

function startElmApp() { 
  var app = Elm.Main.init({
    node: document.getElementById('root'),
    flags: encodedUser,
  });
  app.ports.storeCache.subscribe(function(u) {
    localStorage.setItem('user', JSON.stringify(u));
  });
}

